package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// RestoreResult is one session that restore looked at.
type RestoreResult = api.RestoreResult

// Restore starts again each session that a host restart (or, with all, any
// crash) left without a holder. Each agent resumes its own conversation with
// its resume command, and a session's own command runs again (resumeArgv).
// A turn that was running is not resumed.
func Restore(all bool) []RestoreResult {
	boot := BootID()
	entries, _ := os.ReadDir(paths.SessionsDir())
	var out []RestoreResult
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if r, ok := restoreOne(e.Name(), boot, all); ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// restoreOne looks at one session for Restore, under the session's lock.
// It reports false for a session to leave out of the results: one whose
// holder runs, or one without a manifest.
func restoreOne(id, boot string, all bool) (RestoreResult, bool) {
	m, err := LoadManifest(id)
	if err != nil {
		return RestoreResult{}, false
	}
	r := RestoreResult{ID: id, Name: m.Name}
	unlock, err := lockSession(id)
	if err != nil {
		r.Action, r.Why = "skipped", "a resume, a restore or a delete of it runs"
		return r, true
	}
	defer unlock()
	// Read it again under the lock: a resume may have changed it.
	if m, err = LoadManifest(id); err != nil {
		return RestoreResult{}, false
	}
	switch {
	case alive(id):
		return r, false
	case m.Status.State == StateExited || m.Status.State == StateErrored:
		r.Action, r.Why = "skipped", "the agent had ended"
	case m.Restart.Policy != "resume":
		r.Action, r.Why = "skipped", "restart policy is "+m.Restart.Policy
	case !all && m.Run.BootID == boot:
		r.Action, r.Why = "skipped", "no restart since it stopped (use --all)"
	case m.Restart.MaxAttempts > 0 && m.Run.RespawnCount >= m.Restart.MaxAttempts:
		r.Action, r.Why = "skipped", fmt.Sprintf("already restored %d times", m.Run.RespawnCount)
	default:
		m.Run.RespawnCount++
		argv, why, err := startAgain(m, again{label: restoredLabel, reason: "restored after a host restart"})
		r.Argv = argv
		if err != nil {
			r.Action, r.Why = "failed", err.Error()
			break
		}
		r.Action, r.Why = "restored", why
	}
	return r, true
}

// again says why a session's agent starts again: restore after a host
// restart, or the user's resume (resume.go).
type again struct {
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
// conversation in its folder".
func startAgain(m *Manifest, a again) (argv []string, why string, err error) {
	argv, why = resumeArgv(m)
	m.Agent.Argv = argv
	m.RunHook = m.ResumeHook
	m.Run.BootID = BootID()
	// The old processes are gone. The new holder records its own.
	m.Run.HolderPID, m.Run.AgentPID, m.Run.Unit = 0, 0, ""
	m.Limits.MemoryMax = sessionMemoryMax(m.Limits.MemoryAsked)
	// To the nanosecond: the holder compares it with the last start, which
	// can be in the same second. The other label goes: after a restore
	// that failed to start the agent, a resume must not look like one.
	now := time.Now().UTC()
	delete(m.Labels, restoredLabel)
	delete(m.Labels, resumedLabel)
	m.Labels = withLabel(m.Labels, a.label, now.Format(time.RFC3339Nano))
	m.Status = Status{State: StateStarting, Since: now, Reason: a.reason + ": " + why}
	if err := m.Save(); err != nil {
		return argv, why, err
	}
	if err := launchHolder(m); err != nil {
		return argv, why, err
	}
	if err := deliverSecrets(m.ID, a.secrets); err != nil {
		return argv, why, err
	}
	// The holder answers on its socket before it starts the agent, as New
	// knows too: wait for the start before saying it worked.
	if mm := waitStarted(m.ID, 5*time.Second); mm != nil && mm.Status.State == StateErrored && mm.Run.AgentPID == 0 {
		return argv, why, errors.New(mm.Status.Reason)
	}
	return argv, why, nil
}

// resumeArgv picks how to bring an agent back, in this order:
//
//  1. the agent's resume command with its own session ID, when the table
//     gives a resume command and the session recorded an ID;
//  2. the command that the user gave the session, if any;
//  3. the agent's continue command: "continue the latest in this folder";
//  4. a fresh start with the command of the last start.
//
// A resume by ID keeps the conversation, but drops the extra arguments of
// the user's own command, as before Command. The user's command comes from
// Command, which no start changes. An older manifest has no Command: a
// command without an agent then starts fresh from Argv, which still holds it.
func resumeArgv(m *Manifest) ([]string, string) {
	vars := map[string]string{"agent_session_id": m.Agent.AgentSessionID}
	var argv []string
	why := "started fresh"
	switch {
	case m.Agent.AgentSessionID != "" && len(m.Agent.ResumeArgv) > 0:
		argv, why = agents.Expand(m.Agent.ResumeArgv, vars), "resumed its own session "+m.Agent.AgentSessionID
	case len(m.Agent.Command) > 0:
		argv, why = slices.Clone(m.Agent.Command), "started its own command again"
	case len(m.Agent.ContinueArgv) > 0:
		argv, why = append([]string(nil), m.Agent.ContinueArgv...), "continued the latest conversation in its folder"
	default:
		argv = append([]string(nil), m.Agent.Argv...)
	}
	for i, s := range argv {
		if s == "$SHELL" {
			argv[i] = LoginShell()
		}
	}
	return append(append(argv, m.Agent.Hooks...), m.Agent.Inline...), why
}

func withLabel(l map[string]string, k, v string) map[string]string {
	if l == nil {
		l = map[string]string{}
	}
	l[k] = v
	return l
}

// restoreUnit is the systemd user unit that runs restore at boot.
func restoreUnit() string { return brand.Name + "-restore.service" }

// restoreUnitText is the restore unit's file.
func restoreUnitText() string {
	return fmt.Sprintf(`[Unit]
Description=Restore %s sessions after a restart
After=default.target

[Service]
Type=oneshot
ExecStart=%s restore

[Install]
WantedBy=default.target
`, brand.DisplayName, currentHelper())
}

// InstallRestoreUnit writes and enables the boot-time restore unit.
func InstallRestoreUnit() Check {
	c := Check{Name: "restore after restart"}
	if !SystemdUser() {
		c.Detail = "no systemd user manager; sessions are not restored after a restart"
		return c
	}
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.Detail = err.Error()
		return c
	}
	unit := restoreUnitText()
	path := filepath.Join(dir, restoreUnit())
	if old, err := os.ReadFile(path); err != nil || string(old) != unit {
		//nolint:gosec // G306: systemd warns about a unit file that others cannot read; its text is public through systemd anyway
		if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
			c.Detail = err.Error()
			return c
		}
		_ = exec.CommandContext(context.Background(), "systemctl", "--user", "daemon-reload").Run()
	}
	out, err := exec.CommandContext(context.Background(), "systemctl", "--user", "enable", restoreUnit()).CombinedOutput()
	if err != nil {
		c.Detail = "could not enable it: " + strings.TrimSpace(string(out))
		return c
	}
	c.OK, c.Detail = true, "sessions come back after a host restart"
	return c
}
