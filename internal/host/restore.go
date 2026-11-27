package host

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RestoreResult is one session that restore looked at.
type RestoreResult = api.RestoreResult

// Restore starts again each session that a host restart (or, with all, any
// crash) left without a holder. Each agent resumes its own conversation with
// its resume command, and a session's own command runs again (resumeArgv).
// A turn that was running is not resumed.
func Restore(all bool) []RestoreResult {
	rows, err := RestoreContext(context.Background(), all)
	if err != nil {
		return []RestoreResult{lazyRestoreFailureV1(err)}
	}
	return rows
}

// restoreOneContext looks at one session for Restore, under the session's lock.
// It reports false for a session to leave out of the results: one whose
// holder runs, or one without a manifest.
func restoreOneContext(ctx context.Context, id, boot string, all bool) (RestoreResult, bool) {
	return restoreOneForGateV1(ctx, id, boot, all, nil)
}

func restoreOneForGateV1(ctx context.Context, id, boot string, all bool, gate *restoreGateV1) (RestoreResult, bool) {
	if err := CheckCaller(); err != nil {
		return RestoreResult{ID: id, Action: "failed", Why: err.Error()}, true
	}
	m, err := LoadManifest(id)
	if err != nil {
		return RestoreResult{}, false
	}
	if m.SandboxOf != "" {
		return RestoreResult{}, false
	}
	r := RestoreResult{ID: id, Name: m.Name}
	operation, err := BeginSessionOperation(ctx, id, renameWait)
	if err != nil {
		r.Action, r.Why = "skipped", "a resume, a restore or a delete of it runs"
		return r, true
	}
	defer operation.Close()
	// Read through the same retained native source after ownership.
	if m, err = operation.Load(); err != nil {
		return RestoreResult{}, false
	}
	if holderPendingOrAlive(id, m) || oldHolderRuns(id) {
		return r, false
	}
	if err := reconcileUnsavedLocked(m); err != nil {
		r.Action, r.Why = "failed", err.Error()
		return r, true
	}
	r.Name = m.Name
	if err := validateRestartWall(m); err != nil {
		r.Action, r.Why = "failed", err.Error()
		return r, true
	}
	if IsNoneSupervisor(m) {
		return restoreNoneForGateV1(ctx, gate, operation, m, boot, all)
	}
	var selection *AutomaticRestoreSelection
	if m.Wall != nil && m.Wall.Driver != "plain" {
		selection, err = SelectAutomaticRestore(m)
		if err != nil {
			r.Action, r.Why = "failed", err.Error()
			return r, true
		}
	}
	wanted := !m.Ended()
	if selection != nil {
		wanted = selection.PrimaryWanted || len(selection.MemberIDs) > 0
	}
	switch {
	case !wanted:
		r.Action, r.Why = "skipped", "the agent had ended"
	case m.Restart.Policy != "resume":
		r.Action, r.Why = "skipped", "restart policy is "+m.Restart.Policy
	case !all && m.Run.BootID == boot:
		r.Action, r.Why = "skipped", "no restart since it stopped (use --all)"
	case m.Restart.MaxAttempts > 0 && m.Run.RespawnCount >= m.Restart.MaxAttempts:
		r.Action, r.Why = "skipped", fmt.Sprintf("already restored %d times", m.Run.RespawnCount)
	default:
		if lazy, handled := lazyRestoreDecisionV1(operation, m, all, selection); handled {
			r.Action, r.Why, r.Hint = lazy.Action, lazy.Why, lazy.Hint
			return r, true
		}
		if err := admitRestoreMemoryV1(operation.ownerContext, operation); err != nil {
			r.Action, r.Why = "failed", err.Error()
			return r, true
		}
		m.Run.RespawnCount++
		argv, why, err := startAgain(m, again{operation: operation, automatic: selection, label: restoredLabel, reason: "restored after a host restart", allowMissing: true})
		r.Argv = argv
		if err != nil {
			r.Action, r.Why = "failed", err.Error()
			var pe *proto.Error
			if errors.As(err, &pe) {
				r.Hint = pe.Hint
			}
			break
		}
		r.Action, r.Why = "restored", why
	}
	return r, true
}

// again says why a session's agent starts again: restore after a host
// restart, or the user's resume (resume.go).
type again struct {
	operation      *SessionOperation
	automatic      *AutomaticRestoreSelection
	members        []api.MemberResumeArgs
	allowMissing   bool
	secretDelivery *api.SecretDeliveryIntent
	// label records when, so that the new holder can tell which:
	// restoredLabel or resumedLabel.
	label string
	// reason starts the reason of the session's state until the agent runs.
	reason string
	// secrets go to the new holder before it starts the agent.
	secrets map[string]api.Secret
}

// startAgain starts the agent of session m again, in a new holder. It runs
// the command from resumeArgv and the session's resume hook, under the cap
// that m asks for. The old holder must be gone. It keeps the session's ID,
// name, folder and worktree. It waits for the start, as New does. It
// returns the command and what it does, such as "continued the latest
// conversation in its folder". The caller reconciles any legacy rename
// before applying memory and restore-attempt changes.
func startAgain(m *Manifest, a again) (argv []string, why string, err error) {
	if err := ValidateManifestStart(m); err != nil {
		return nil, "", err
	}
	if m.Wall == nil && WallDriver != "legacy-none-test" {
		if err := prepareLegacyNoneTransitionV1(a.operation, m); err != nil {
			return nil, "", err
		}
	}
	if a.automatic != nil && !a.automatic.PrimaryWanted {
		argv = slices.Clone(m.Agent.Argv)
		why = "restored its selected sandbox members"
	} else {
		argv, why, err = resumeArgv(m)
		if err != nil {
			return nil, why, err
		}
	}
	dropUnusableID(m)
	m.Agent.Argv = argv
	m.Env = withHookSocket(m.Env, m.ID)
	m.RunHook = resumeHook(m)
	if m.Wall == nil && a.secretDelivery != nil {
		m.SecretState = initialSecretState(a.secretDelivery)
	}
	applySessionIdentity(m)
	m.Run.BootID = BootID()
	// The old processes are gone. The new holder records its own.
	m.Run.HolderPID, m.Run.AgentPID, m.Run.Unit = 0, 0, ""
	m.NoneCleanup = nil
	if a.operation != nil {
		if err := applyAdmittedRestartMemoryV1(a.operation, m); err != nil {
			return nil, why, err
		}
	} else {
		m.Limits.MemoryMax = sessionMemoryMax(m.Limits.MemoryAsked)
	}
	// To the nanosecond: the holder compares it with the last start, which
	// can be in the same second. The other label goes: after a restore
	// that failed to start the agent, a resume must not look like one.
	now := time.Now().UTC()
	if a.label == resumedLabel {
		m.AutomaticRestore = nil
	}
	delete(m.Labels, restoredLabel)
	delete(m.Labels, resumedLabel)
	m.Labels = withLabel(m.Labels, a.label, now.Format(time.RFC3339Nano))
	m.Status = Status{State: StateStarting, Since: now, Reason: a.reason + ": " + why}
	nativeWall := m.Wall != nil && m.Wall.Driver != "plain"
	envelope := m.Wall != nil && (nativeWall || a.label != restoredLabel)
	m.Run.StartEnvelope = envelope && (a.allowMissing || a.members != nil || a.secretDelivery != nil)
	if a.automatic != nil {
		if err := PinAutomaticRestore(m, a.automatic); err != nil {
			return argv, why, err
		}
	}
	if a.operation != nil {
		if err := saveRestartManifestV1(a.operation, m); err != nil {
			return argv, why, err
		}
	} else if err := m.Save(); err != nil {
		return argv, why, err
	}
	if err := finishLegacyNoneTransitionV1(a.operation); err != nil {
		return argv, why, err
	}
	if nativeWall {
		if a.operation == nil {
			return argv, why, sessionLeaseError("the real walled restart has no original owning operation")
		}
		if a.automatic != nil {
			if err := prepareAutomaticBootOperationV1(a.operation); err != nil {
				return argv, why, err
			}
		}
		delivery := a.secretDelivery
		if delivery == nil {
			delivery = initialDelivery(m)
		}
		started, launch, startErr := startOwnedSessionOperation(a.operation.ownerContext, a.operation, a.secrets, a.members, a.allowMissing, delivery)
		if launch != nil {
			startErr = errors.Join(startErr, launch.Close())
		}
		if startErr != nil {
			return argv, why, startErr
		}
		if started == nil {
			return argv, why, sessionLeaseError("the original restart returned no durable native readiness")
		}
		*m = *started
		return argv, why, nil
	}
	proc, err := startHolder(m)
	if err != nil && a.label == restoredLabel && (proc != nil || m.Run.Unit != "") {
		// A restored holder never waits for secrets (secretsBeforeStart),
		// so it cannot start its agent without them. A slow one, as at a
		// busy boot, is not stopped: it gets more time.
		lateStart(m.ID)
		if err = waitSocket(m.ID, logSize(m.ID), restoreWait); err != nil {
			return argv, why, finishSlowRestore(m, proc, err)
		}
	}
	if err != nil {
		// A holder that started, and did not answer in time, could still
		// start its agent without its secrets: it stops first.
		if proc != nil || m.Run.Unit != "" {
			if serr := stopStarting(m, proc); serr != nil {
				return argv, why, fmt.Errorf("%w, and its holder could not be stopped, so its agent may start later without its secrets: %w", err, serr)
			}
			_ = os.Remove(paths.Socket(m.ID))
		}
		if proc != nil {
			_ = proc.Release()
		}
		return argv, why, err
	}
	var delivery *api.SecretDeliveryIntent
	if envelope {
		delivery = a.secretDelivery
	}
	if err := deliverStart(m.ID, a.secrets, a.members, envelope && a.allowMissing, delivery); err != nil {
		// The new holder waits for its secrets. It stops first, so that its
		// agent never starts without them, as after a failed start.
		if serr := stopStarting(m, proc); serr != nil {
			return argv, why, fmt.Errorf("%w, and its holder could not be stopped, so its agent may start without them: %w", err, serr)
		}
		_ = os.Remove(paths.Socket(m.ID))
		if proc != nil {
			_ = proc.Release()
		}
		return argv, why, err
	}
	if proc != nil {
		_ = proc.Release()
	}
	if m.Wall != nil {
		if _, err := waitBoot(m.ID); err != nil {
			return argv, why, err
		}
		return argv, why, nil
	}
	// The holder answers on its socket before it starts the agent, as New
	// knows too: wait for the start before saying it worked.
	if mm := waitStarted(m.ID, 5*time.Second); mm != nil && mm.Status.State == StateErrored && mm.Run.AgentPID == 0 {
		return argv, why, errors.New(mm.Status.Reason)
	}
	return argv, why, nil
}

// resumeHook is the hook that runs before the agent of m starts again: the
// session's resume hook. When that is the repository's own script, the host
// reads the scripts again first, at the top level of the session's folder
// (spec 19.2). If their setup hash differs from the one that the session
// started with, or the session kept none, the script is skipped, and the
// host's own hook runs, as for a repository that is not trusted. A line in
// the session's log says so.
func resumeHook(m *Manifest) string {
	hook := m.ResumeHook
	// The repository's script is <top>/.<name>/resume. Any other hook is
	// the host's own, which is the user's: it is never checked. The path of
	// the host's hooks depends on the environment of the moment, so it is
	// not the test.
	if hook == "" || filepath.Base(filepath.Dir(hook)) != "."+brand.Name {
		return hook
	}
	// git answers in a git operation's child. When it cannot, as for
	// git_scope, the script is not checked, so it does not run.
	repo, top, err := repoOf(m.Cwd)
	var why string
	switch {
	case err != nil:
		repo, why = m.Cwd, "could not find its repository: "+err.Error()
	case m.SetupHash == "":
		why = "the session kept no setup hash to check it against"
	default:
		switch s, err := readScripts(top); {
		case err != nil:
			why = "could not check it: " + err.Error()
		case s.hash != m.SetupHash || s.resume != hook:
			why = "it changed after the start"
		default:
			return hook
		}
	}
	warnSession(m.ID, "skipped the resume script of "+repo+": "+why)
	return hostHook("resume")
}

// finishSlowRestore records a slow restored holder's ownership in
// manifest.json, or stops the holder via proc when that record cannot be
// saved.
func finishSlowRestore(m *Manifest, proc *os.Process, waitErr error) error {
	if !holderEnded(m.ID) {
		if perr := persistPendingHolder(m); perr != nil {
			if serr := stopStarting(m, proc); serr != nil {
				return fmt.Errorf("%w, could not record its holder (%w), and could not stop it: %w", waitErr, perr, serr)
			}
			_ = os.Remove(paths.Socket(m.ID))
			if proc != nil {
				_ = proc.Release()
			}
			return fmt.Errorf("%w, so its holder was stopped: %w", waitErr, perr)
		}
		waitErr = fmt.Errorf("%w, and its holder may still start", waitErr)
	}
	if proc != nil {
		_ = proc.Release()
	}
	return waitErr
}

// writePendingManifest writes a slow restored holder's manifest record.
// Tests replace it to inject a save failure.
var writePendingManifest = func(m *Manifest) error { return m.Save() }

// persistPendingHolder records a slow restored holder's process or unit in
// manifest.json under first.lock before restoreOne releases op.lock, so a
// later Delete or Resume can detect that holder before its socket opens.
// The recorded PID is observation only, never permission to signal it.
func persistPendingHolder(m *Manifest) error {
	if m.Run.HolderPID == 0 && m.Run.Unit == "" {
		return errors.New("no holder process or unit recorded")
	}
	unlock, err := lockFirstSave(m.ID, renameWait)
	if err != nil {
		return fmt.Errorf("lock first save of session %s: %w", m.ID, err)
	}
	defer unlock()
	diskM, err := LoadManifest(m.ID)
	if err != nil {
		return fmt.Errorf("load manifest of session %s: %w", m.ID, err)
	}
	if diskM.Status.State != StateStarting || diskM.Run.AgentPID != 0 {
		return nil
	}
	diskM.Run.HolderPID = m.Run.HolderPID
	if m.Run.Unit != "" {
		diskM.Run.Unit = m.Run.Unit
	}
	return writePendingManifest(diskM)
}

// resumeArgv picks how to bring an agent back, in this order:
//
//  1. the agent's resume command with its own session ID, when the table
//     gives a resume command and the session recorded an ID that may enter
//     it (idUnusable);
//  2. the command that the user gave the session, if any;
//  3. the agent's continue command: "continue the latest in this folder";
//  4. a fresh start with the command of the last start.
//
// A resume by ID keeps the conversation, but drops the extra arguments of
// the user's own command, as before Command. The user's command comes from
// Command, which no start changes. An older manifest has no Command: a
// command without an agent then starts fresh from Argv, which still holds it.
//
// A resume command that an older helper saved without its "--" gets the
// agent's current one (agents.Agent.ResumeTemplate). The hooks' and the
// inline mode's arguments go before a "--" in the command, so that the ID
// after it stays a positional argument. The command is refused when it
// holds a run of arguments that turns the agent's own approvals off
// (agents.Agent.Dangers), unless the user's own command holds the same run.
func resumeArgv(m *Manifest) ([]string, string, error) {
	agent, _ := agents.Find(m.Agent.Kind)
	vars := map[string]string{"agent_session_id": m.Agent.AgentSessionID}
	var argv []string
	why := "started fresh"
	switch {
	case len(m.Agent.ResumeArgv) > 0 && idUnusable(agent, m.Agent) == nil:
		argv = agents.Expand(agent.ResumeTemplate(m.Agent.ResumeArgv), vars)
		why = "resumed its own session " + m.Agent.AgentSessionID
	case len(m.Agent.Command) > 0:
		argv, why = slices.Clone(m.Agent.Command), "started its own command again"
	case len(m.Agent.ContinueArgv) > 0:
		argv, why = append([]string(nil), m.Agent.ContinueArgv...), "continued the latest conversation in its folder"
	default:
		argv = append([]string(nil), m.Agent.Argv...)
	}
	if m.Wall != nil && (m.Wall.Driver == "chv" || m.Wall.Driver == "firecracker" || m.Wall.Driver == "nitro") {
		argv = vmCommand(argv)
	} else {
		for i, s := range argv {
			if s == "$SHELL" {
				argv[i] = LoginShell()
			}
		}
	}
	argv = withOptions(argv, m.Agent.Hooks, m.Agent.Inline)
	for _, sp := range agent.Dangers(argv) {
		run := argv[sp.I : sp.I+sp.N]
		if !holdsRun(m.Agent.Command, run) {
			return nil, why, UserError(dangerRefused,
				fmt.Sprintf("%s did not start again: you did not type %q, which turns the agent's own approvals off", m.Name, clip(strings.Join(run, " "), 60)),
				"Start a new session, and put that argument in its command if you want it.")
		}
	}
	return argv, why, nil
}

// dangerRefused is the code of the error of resumeArgv.
const dangerRefused = "danger_refused"

// holdsRun reports whether cmd holds run, as consecutive arguments.
func holdsRun(cmd, run []string) bool {
	for i := 0; i+len(run) <= len(cmd); i++ {
		if slices.Equal(cmd[i:i+len(run)], run) {
			return true
		}
	}
	return false
}

// errNoID is idUnusable's reason when the session recorded no ID.
var errNoID = errors.New("the session recorded no agent session ID")

// idUnusable says why a.AgentSessionID, the ID that the session recorded,
// may not enter the resume command of agent, or returns nil when it may. A
// hook can report any text as the ID, such as a flag, and an older helper
// kept it. So the ID must match the agent's IDPattern, wherever it came
// from. An ID that the host chose itself needs no match (spec 8.6).
func idUnusable(agent agents.Agent, a AgentInfo) error {
	switch {
	case a.AgentSessionID == "":
		return errNoID
	case a.IDSource == "chosen":
		return nil
	}
	return agent.CheckID(a.AgentSessionID)
}

// dropUnusableID drops the agent session ID that session m recorded when
// it may not enter a resume command (idUnusable). The session then comes
// back without it, and the session's holder log says why.
func dropUnusableID(m *Manifest) {
	agent, _ := agents.Find(m.Agent.Kind)
	why := idUnusable(agent, m.Agent)
	if why == nil || errors.Is(why, errNoID) {
		return
	}
	if l, err := openHolderLog(paths.HolderLog(m.ID), holderLogMax); err == nil {
		newHolderLog(l, nil).Warn("saved session ID not used", "reason", why.Error(), "kind", m.Agent.Kind, "id", clip(m.Agent.AgentSessionID, 80))
		_ = l.Close()
	}
	m.Agent.AgentSessionID, m.Agent.IDSource = "", ""
}
