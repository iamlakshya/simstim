package host

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// startHolder is the owned holder launcher. Tests replace it.
var startHolder = launchHolder

func startSession(m *Manifest, secrets map[string]api.Secret, fromFolder ...bool) (*SessionInfo, error) {
	reservation, err := reserveSessionID(m.ID)
	if err != nil {
		return nil, err
	}
	defer reservation.close()
	if m.ID == "" {
		m.ID = reservation.id
	}
	return startReservedSession(m, secrets, reservation, fromFolder...)
}

func startReservedSession(m *Manifest, secrets map[string]api.Secret, reservation *sessionReservation, fromFolder ...bool) (*SessionInfo, error) {
	operation, err := beginReservedSessionOperation(context.Background(), reservation)
	if err != nil {
		return nil, err
	}
	defer func() {
		reservation.close()
		operation.Close()
	}()
	return startReservedSessionWithOperation(m, secrets, reservation, operation, fromFolder...)
}

// startReservedSessionWithOperation consumes the original reservation's held OP.
// New keeps it across its first save, launch, delivery, readiness and original
// failure cleanup. The child must borrow that same lock, never reacquire it.
func startReservedSessionWithOperation(m *Manifest, secrets map[string]api.Secret, reservation *sessionReservation, operation *SessionOperation, fromFolder ...bool) (*SessionInfo, error) {
	if m == nil || reservation == nil || reservation.dir == nil || operation == nil ||
		reservation.id != m.ID || !operation.Valid(m.ID) || operation.native.borrowed {
		return nil, sessionLeaseError("startup has no original reserved primary operation")
	}
	if err := operation.native.rootRef.CheckRoot(reservation.dir); err != nil {
		return nil, err
	}
	id := m.ID
	derived := len(fromFolder) > 0 && fromFolder[0]
	var err error
	// No session's ID is the name: an address with it would reach that
	// session first (cli-spec 9.2). derived is true for a name from the
	// folder.
	if m.Name, err = freeName(id, m.Name, derived); err != nil {
		return nil, err
	}
	if m.SandboxOf == "" && m.SandboxName == "" {
		m.SandboxName = m.Name
	}
	applySessionIdentity(m)
	nativeWall := m.Wall != nil
	if m.Wall != nil && m.Wall.Driver == "plain" {
		if m.Wall.Level != "none" {
			return nil, sessionLeaseError("the explicit no-wall test driver has an inconsistent level")
		}
		nativeWall = false
	}
	if nativeWall {
		m.Run.StartEnvelope = true
	}
	if err := saveReservedManifest(operation, m); err != nil {
		return nil, err
	}
	logSkippedCopies(id, m.Git)
	if m.Files != nil {
		if err := seedConnectionAlive(); err != nil {
			return nil, err
		}
	}
	m.launchStarted = false
	if nativeWall {
		return startOwnedReservedSession(m, secrets, reservation, operation)
	}
	reservation.launched = m.Files == nil
	proc, err := startHolder(m)
	if m.launchStarted || proc != nil || m.Run.Unit != "" {
		reservation.launched = true
	}
	waiting := false // a Wait on proc goes on: it must not be released
	defer func() {
		if proc != nil && !waiting {
			_ = proc.Release()
		}
	}()
	if err != nil {
		lateStart(id)
	}
	if err != nil && (proc == nil && m.Run.Unit == "" || holderEnded(id)) {
		return nil, UserError("start_failed", err.Error(), "Check the session log with the host doctor.")
	}
	if err != nil {
		// The holder started, and did not answer in time: it could still
		// start its agent, without its secrets. It stops, and the session
		// goes, only once the holder is known to have stopped.
		if kept := abandonStart(m, proc, "start_failed", "the holder did not answer in time: "+err.Error()); kept != nil {
			waiting = true
			return nil, kept
		}
		return nil, UserError("start_failed", err.Error()+", so the session was removed", "Try again. If it goes on, check the host with the host doctor.")
	}
	var delivery *api.SecretDeliveryIntent
	if m.Wall != nil && m.SecretState != nil {
		copy := api.CloneSecretDeliveryIntent(m.SecretState.Intent)
		delivery = &copy
	}
	if err := deliverStart(id, secrets, nil, false, delivery); err != nil {
		if kept := abandonStart(m, proc, "secrets_failed", "the session could not get its secrets"); kept != nil {
			waiting = true
			return nil, kept
		}
		return nil, secretsFailed(err)
	}
	var started *Manifest
	if m.Wall != nil {
		started, err = waitBoot(id)
		if err != nil {
			return nil, err
		}
	} else {
		started = waitStarted(id, 5*time.Second)
	}
	if mm := started; mm != nil {
		if m.Wall == nil && mm.Status.State == StateErrored && mm.Run.AgentPID == 0 {
			// The holder answered on its socket, as it does before the agent
			// starts, and then could not start the agent.
			return nil, UserError("start_failed", mm.Status.Reason, "Check the session log with the host doctor.")
		}
		// The holder records its own unit (ownUnit), as after a resume. No
		// save here could put back a state older than the holder's own.
		m = mm
	}
	info := publicationInfoFrom(m, true)
	if err := checkSecretPublicationResponse(info); err != nil {
		return nil, err
	}
	return &info, nil
}
func infoFrom(m *Manifest, alive bool) SessionInfo {
	st := m.Status
	if !alive && !m.Ended() {
		st.Claimed = false
		st.State = StateLost
		if m.Run.BootID != "" && m.Run.BootID != BootID() {
			st.Reason = "the host restarted"
		} else {
			st.Reason = "the session's holder stopped"
		}
	}
	return SessionInfo{
		CopiesPending: m.CopiesPending, ID: m.ID, Name: m.Name, Task: m.Task, SandboxOf: m.SandboxOf, Agent: m.Agent.Kind, Cwd: m.Cwd, Workspace: workspaceMode(m),
		State: st.State, Source: st.Source, Since: st.Since, Reason: st.Reason, ExitCode: st.ExitCode, Signal: st.Signal, OOM: st.OOM,
		CreatedAt: m.CreatedAt, LastActivityAt: m.LastActivityAt, LastAttachedAt: m.LastAttachedAt,
		Alive: alive, Unit: m.Run.Unit, MemoryMax: m.Limits.MemoryMax, MemoryCurrent: memoryCurrent(m.Run.Unit),
		HostVersion: m.HostVersion, Cols: m.Term.Cols, Rows: m.Term.Rows, MissingSecrets: m.MissingSecrets,
		Secrets: m.Secrets, SecretSummary: secretSummaryFrom(m), FullScreen: st.FullScreen && alive,
		DebugUntil: debugUntil(m, alive),
		Wall:       wallInfo(m.Wall), Claimed: st.Claimed, Armed: m.Armed,
	}
}
