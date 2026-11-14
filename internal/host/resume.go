package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The user's resume: the agent of a session starts again in the same
// session, after it ended. It takes the path of restore for one session
// (startAgain), with these differences:
//
//   - It takes any session whose agent is not running. That is one that
//     exited, even with code 0, one that failed or that the memory cap
//     stopped, and one whose holder was lost. The user asks for this
//     session, so nothing about why it ended needs to hold it back.
//   - It is not one of restore's tries after a host restart. It starts the
//     count of those again, so restore brings the session back after the
//     next restart.
//   - It labels the session resumed_at, not restored_at. The session then
//     sends a started alert, not a restored one. It gets its secrets from
//     the client, as at a new start.
//   - It holds the session's lock, as restore and delete do, so that only
//     one of them stops or starts the session's holder at a time.
//   - It stops the old holder and its unit first, and with the unit every
//     process that the agent left there, with the memory they hold.

// ResumeResult is what a resume did.
type ResumeResult = api.ResumeResult

// Resume starts the agent of session a.ID again, after it ended, with the
// agent's own resume command, or the session's own command (resumeArgv).
// It keeps the session's ID, name, folder, worktree and manifest. The cap
// is the size asked for now, else the size the session asked for before,
// else the host's default.
func Resume(a api.ResumeArgs) (*ResumeResult, error) {
	id := a.ID
	if !validID(id) {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if err := checkMemoryMax(a.MemoryMax); err != nil {
		return nil, err
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	// A second resume of the session, such as a key pressed twice, would
	// stop the first's new holder, or save over its manifest.
	unlock, err := lockSession(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	m, err := LoadManifest(id)
	if err != nil {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	pending := m.Status.State == StateStarting && m.Run.AgentPID == 0
	if alive(id) && m.Status.State != StateExited && m.Status.State != StateErrored && !pending {
		name := m.Name
		if settled, ok := settledRenameName(id); ok {
			name = settled
		}
		return nil, UserError("agent_running", "the agent of "+name+" still runs",
			"Attach to it. To start it again, quit the agent first, then resume the session.")
	}
	secrets, err := resumeSecrets(m.Secrets, a.Secrets)
	if err != nil {
		return nil, err
	}
	// A command that resumeArgv refuses stops nothing. startAgain checks
	// it again, after the old holder's last save.
	if _, _, err := resumeArgv(m); err != nil {
		return nil, err
	}
	if pending {
		if err := stopPendingForResume(id, m); err != nil {
			return nil, err
		}
	}
	// The session's debug stops with its old holder (holder_debug.go).
	debugOn := debugUntil(m, alive(id)) != nil
	if err := stopResumeHolder(id, m, pending); err != nil {
		return nil, UserError("busy", fmt.Sprintf("the previous holder of session %s could not be stopped: %v", id, err),
			"Try again in a moment, or check the host with the host doctor.")
	}
	if oldHolderRuns(id) {
		return nil, UserError("busy", fmt.Sprintf("the previous holder of session %s is still running", id),
			"Try again in a moment, or check the host with the host doctor.")
	}
	// The old holder saves the manifest a last time as it stops.
	if m, err = LoadManifest(id); err != nil {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if err := reconcileUnsavedLocked(m); err != nil {
		return nil, UserError("start_failed", err.Error(), "Check the session log with the host doctor.")
	}
	switch {
	case a.MemoryMax > 0:
		m.Limits.MemoryAsked = a.MemoryMax
	case a.MemoryMax < 0:
		m.Limits.MemoryAsked = 0
	}
	m.Run.RespawnCount = 0
	_, why, err := startAgain(m, again{label: resumedLabel, reason: "started again by a resume", secrets: secrets})
	var refused *proto.Error
	switch {
	case errors.As(err, &refused) && refused.Code == dangerRefused:
		return nil, refused // resumeArgv refused the command, with its own hint
	case err != nil:
		return nil, UserError("start_failed", err.Error(), "Check the session log with the host doctor.")
	}
	if mm := waitStarted(id, time.Second); mm != nil {
		m = mm
	}
	return &ResumeResult{Session: infoFrom(m, alive(id)), Why: why, DebugStopped: debugOn}, nil
}

// resumeSecrets checks the secrets that a resume brings, and returns those
// the session asked for at its start, want. A missing one would leave the
// new holder waiting for it, so the resume stops first.
func resumeSecrets(want []string, got map[string]api.Secret) (map[string]api.Secret, error) {
	if _, err := checkSecrets(got); err != nil {
		return nil, err
	}
	out := make(map[string]api.Secret, len(want))
	var missing []string
	for _, k := range want {
		v, ok := got[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		out[k] = v
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, UserError("secrets_missing", "the session needs its secrets again: "+strings.Join(missing, ", "),
			"Resume it from the client, which fetches them from config.toml.")
	}
	return out, nil
}

// stopWait is how long stopHolder waits for a holder to go, at each step.
// Tests shorten it.
var stopWait = 5 * time.Second

// stopHolder stops the holder of session id, then its unit, and with the
// unit every process that the agent left there. m is the session's
// manifest, or nil when it cannot be read. It returns an error when a live
// or starting holder could not be stopped.
func stopHolder(id string, m *Manifest) error {
	if err := stopOverSocket(id, m); err != nil {
		return err
	}
	if m != nil && m.Run.Unit != "" {
		if m.Run.Unit != UnitName(id) {
			return fmt.Errorf("the recorded unit does not belong to session %s", id)
		}
		if !SystemdUser() {
			return fmt.Errorf("the service manager cannot confirm the stop of session %s", id)
		}
		err := stopUnitChecked(m.Run.Unit)
		waitUnitUnloaded(m.Run.Unit)
		if err != nil {
			return err
		}
	}
	// A lock proves that a holder exists, not that its recorded PID still
	// names that process. Recovery never signals or reaps that bare PID.
	// Only a launch that owns its *os.Process may stop it directly.
	deadline := time.Now().Add(stopWait)
	for holderStillRuns(id, m) && time.Now().Before(deadline) {
		holderStopWaiting(id)
		time.Sleep(50 * time.Millisecond)
	}
	if holderStillRuns(id, m) {
		return fmt.Errorf("the holder of session %s has not confirmed its stop", id)
	}
	_ = os.Remove(paths.Socket(id))
	_ = os.Remove(paths.HookSocket(id))
	return nil
}

func holderStillRuns(id string, m *Manifest) bool {
	return alive(id) || holderLockHeld(id) || recordedProcessRuns(id, m)
}

// holderStopWaiting lets tests release a legacy holder after its socket
// closes but before its final save and process exit.
var holderStopWaiting = func(string) {}

// stopOverSocket allows an acknowledged legacy stop to finish without
// signalling the recorded PID after its socket has gone.
func stopOverSocket(id string, m *Manifest) error {
	if alive(id) {
		_, requestErr := HolderRequest(id, "kill", nil, 2*time.Second)
		deadline := time.Now().Add(stopWait)
		for (alive(id) || holderLockHeld(id) || requestErr == nil && recordedProcessRuns(id, m)) && time.Now().Before(deadline) {
			holderStopWaiting(id)
			time.Sleep(50 * time.Millisecond)
		}
		if requestErr == nil && !alive(id) && !holderLockHeld(id) && recordedProcessRuns(id, m) {
			return fmt.Errorf("the holder of session %s accepted its stop but is still running", id)
		}
	}
	return nil
}

// recordedProcessRuns observes the old holder after a successful stop reply.
// It never signals or reaps a process whose socket has already closed.
func recordedProcessRuns(id string, m *Manifest) bool {
	return m != nil && (m.Run.BootID == "" || m.Run.BootID == BootID()) && holderPIDAlive(m.Run.HolderPID, id)
}

// stopUnit stops a session's unit, with every process in it. It then waits
// a moment until systemd has unloaded the unit, which --collect asks for,
// so that a new holder can take its name.
func stopUnit(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = systemctl(ctx, "stop", unit)
	waitUnitUnloaded(unit)
}

func waitUnitUnloaded(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := systemctl(ctx, "show", unit, "-p", "LoadState", "--value")
		if err != nil || strings.TrimSpace(string(out)) != "loaded" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
