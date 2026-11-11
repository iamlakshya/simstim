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
	"github.com/iamlakshya/simstim/internal/proto"
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
	argv, why, err = resumeArgv(m)
	if err != nil {
		return nil, why, err
	}
	dropUnusableID(m)
	m.Agent.Argv = argv
	m.Env = withHookSocket(m.Env, m.ID)
	m.RunHook = resumeHook(m)
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
	repo, top := repoOf(m.Cwd)
	var why string
	if m.SetupHash == "" {
		why = "the session kept no setup hash to check it against"
	} else {
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
	for i, s := range argv {
		if s == "$SHELL" {
			argv[i] = LoginShell()
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

// withOptions returns argv with the arguments of opts added: before the
// first "--", which ends the agent's options, or else at the end.
func withOptions(argv []string, opts ...[]string) []string {
	extra := slices.Concat(opts...)
	if i := slices.Index(argv, "--"); i >= 0 {
		return slices.Concat(argv[:i], extra, argv[i:])
	}
	return slices.Concat(argv, extra)
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
