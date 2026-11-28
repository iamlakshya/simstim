package host

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

// StopResult is what a stop did.
type StopResult = api.StopResult

// stopHookTimeout bounds how long a session's stop hook may run before it is
// killed so that stop always completes. Tests shorten it.
var stopHookTimeout = 10 * time.Second

// Stop stops a session's holder and systemd user unit while keeping its
// folder, manifest, worktree and conversation state on disk for a later
// resume or restore. Stopping an already stopped session succeeds and reports
// AlreadyStopped.
func Stop(a api.StopArgs) (*StopResult, error) {
	id := a.ID
	if !validID(id) {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	unlock, err := lockSession(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	m, err := LoadManifest(id)
	if err != nil {
		return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
	}
	running := alive(id)
	if !running && m.Status.State == StateStopped {
		if m.Run.Unit != "" {
			stopUnit(m.Run.Unit)
			m.Run.Unit = ""
			_ = m.Save()
		}
		return &StopResult{Session: infoFrom(m, false), AlreadyStopped: true}, nil
	}
	if running && !isWalled(m) {
		runStopHook(m)
	}
	stopHolder(id, m)
	// Reload the manifest: the holder saves a last time as it stops, keeping
	// any updated agent session ID or activity timestamp.
	if reloaded, err := LoadManifest(id); err == nil {
		m = reloaded
	}
	now := time.Now().UTC()
	m.Run.HolderPID = 0
	m.Run.AgentPID = 0
	m.Run.Unit = ""
	m.Run.RespawnCount = 0
	m.DebugUntil = nil
	m.Status = Status{
		State:  StateStopped,
		Since:  now,
		Source: "user",
		Reason: "stopped by request",
	}
	if err := m.Save(); err != nil {
		return nil, fmt.Errorf("save the manifest of session %s: %w", id, err)
	}
	return &StopResult{Session: infoFrom(m, false)}, nil
}

// stopHook returns the stop script that may run for m, or "". When the hook is
// a repository's own .<name>/stop, it verifies the setup hash first, just as
// resumeHook does for .<name>/resume (spec 19.2).
func stopHook(m *Manifest) string {
	hook := m.StopHook
	if hook == "" || filepath.Base(filepath.Dir(hook)) != "."+brand.Name {
		return hook
	}
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
		case s.hash != m.SetupHash || s.stop != hook:
			why = "it changed after the start"
		default:
			return hook
		}
	}
	warnSession(m.ID, "skipped the stop script of "+repo+": "+why)
	return hostHook("stop")
}

// runStopHook executes m's verified stop hook in m.Cwd with a bounded timeout.
// A failing or slow hook is logged and never blocks the stop.
func runStopHook(m *Manifest) {
	hook := stopHook(m)
	if hook == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopHookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, hook)
	cmd.Dir = m.Cwd
	cmd.Env = append(append([]string(nil), m.Env...), stopHookVars(m)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		warnSession(m.ID, "stop hook did not start", "hook", hook, "err", err)
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			warnSession(m.ID, "stop hook failed", "hook", hook, "err", err)
		}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		warnSession(m.ID, "stop hook timed out", "hook", hook)
	}
}
