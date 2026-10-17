package host

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
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
	if alive(id) && m.Status.State != StateExited && m.Status.State != StateErrored {
		return nil, UserError("agent_running", "the agent of "+m.Name+" still runs",
			"Attach to it. To start it again, quit the agent first, then resume the session.")
	}
	secrets, err := resumeSecrets(m.Secrets, a.Secrets)
	if err != nil {
		return nil, err
	}
	// The session's debug stops with its old holder (holder_debug.go).
	debugOn := debugUntil(m, alive(id)) != nil
	stopHolder(id, m)
	// The old holder saves the manifest a last time as it stops.
	if m, err = LoadManifest(id); err != nil {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	switch {
	case a.MemoryMax > 0:
		m.Limits.MemoryAsked = a.MemoryMax
	case a.MemoryMax < 0:
		m.Limits.MemoryAsked = 0
	}
	m.Run.RespawnCount = 0
	_, why, err := startAgain(m, again{label: resumedLabel, reason: "started again by a resume", secrets: secrets})
	if err != nil {
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
// manifest, or nil when it cannot be read.
func stopHolder(id string, m *Manifest) {
	if alive(id) {
		_, _ = HolderRequest(id, "kill", nil, 2*time.Second)
		deadline := time.Now().Add(stopWait)
		for alive(id) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	switch {
	case m != nil && m.Run.Unit != "":
		stopUnit(m.Run.Unit)
	case m != nil && m.Run.HolderPID > 0 && alive(id):
		killHolder(m.Run.HolderPID)
	}
	os.Remove(paths.Socket(id))
}

// killHolder stops a holder that did not stop when asked: SIGTERM first,
// then SIGKILL when it is still there after stopWait. It returns only once
// the holder is gone, or after stopWait more. The holder saves the manifest
// and removes its socket as it stops. Both must come before a new holder
// starts.
func killHolder(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitGone(pid, stopWait) {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitGone(pid, stopWait)
}

// waitGone waits until the process pid has exited, for timeout at most. It
// reaps the process when it is a child of this one, since a child that is
// not reaped still answers kill(pid, 0). It reports whether it is gone.
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		var ws syscall.WaitStatus
		if p, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); p == pid {
			return true
		}
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopUnit stops a session's unit, with every process in it. It then waits
// a moment until systemd has unloaded the unit, which --collect asks for,
// so that a new holder can take its name.
func stopUnit(unit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = systemctl(ctx, "stop", unit)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out, err := systemctl(ctx, "show", unit, "-p", "LoadState", "--value")
		if err != nil || strings.TrimSpace(string(out)) != "loaded" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}
