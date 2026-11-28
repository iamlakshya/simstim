package host

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
)

// SpawnSocketRequest is the payload sent by the host helper to a running
// supervisor's session socket on the "spawn" op (isolation spec 6.11, 10.4).
type SpawnSocketRequest struct {
	Manifest  Manifest `json:"manifest"`
	SecretEnv []string `json:"secret_env,omitempty"`
	Notes     []string `json:"notes,omitempty"`
	Ephemeral bool     `json:"ephemeral,omitempty"`
}

// sandboxRootID returns the primary session ID for m's sandbox.
func sandboxRootID(m *Manifest) string {
	if m != nil && m.SandboxOf != "" {
		return m.SandboxOf
	}
	if m != nil {
		return m.ID
	}
	return ""
}

// sandboxChildren returns all persisted child session manifests belonging to
// primaryID, sorted oldest-first.
func sandboxChildren(primaryID string) []*Manifest {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return nil
	}
	var out []*Manifest
	for _, e := range entries {
		if !e.IsDir() || e.Name() == primaryID {
			continue
		}
		m, err := LoadManifest(e.Name())
		if err != nil || m.SandboxOf != primaryID {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// SpawnInSandbox starts a new session inside an existing running walled
// session's sandbox without restarting it (isolation spec 6.11).
func SpawnInSandbox(a NewArgs) (*SessionInfo, error) {
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if err := checkNewArgs(a); err != nil {
		return nil, err
	}
	primary, err := loadRunningSandboxPrimary(a.In)
	if err != nil {
		return nil, err
	}
	secrets, err := checkSecrets(a.Secrets)
	if err != nil {
		return nil, err
	}
	argv, kind, agent, known, hooks, sessionUUID, inline, err := mapJoinedAgentArgv(primary, a)
	if err != nil {
		return nil, err
	}

	id := unusedID()
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = defaultChildName(primary.Name, primary.ID, kind)
	}
	childEnv, term := buildChildEnv(a, agent, inline)
	secretEnv := encodeSecretEnv(a.Secrets)
	defer clear(secretEnv)

	now := time.Now().UTC()
	var wallCopy *WallRecord
	if primary.Wall != nil {
		wc := *primary.Wall
		wallCopy = &wc
	}
	cm := &Manifest{
		Schema:      1,
		ID:          id,
		Name:        name,
		SandboxOf:   primary.ID,
		Task:        strings.TrimSpace(a.Task),
		CreatedAt:   now,
		HostVersion: version.Version,
		Agent:       AgentInfo{Kind: kind, Argv: argv},
		Cwd:         primary.Cwd,
		Env:         childEnv,
		Term:        TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:      primary.Limits,
		Run:         Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:      Status{State: StateStarting, Since: now},
		Restart:     Restart{Policy: "resume", MaxAttempts: 3},
		Wall:        wallCopy,
		Secrets:     secrets,
	}
	fillAgentManifest(cm, a.Argv, argv, agent, known, inline, hooks, sessionUUID)

	if err := paths.EnsurePrivateDir(paths.SessionDir(id)); err != nil {
		return nil, err
	}
	if err := cm.Save(); err != nil {
		return nil, err
	}

	raw, err := HolderRequest(primary.ID, "spawn", SpawnSocketRequest{
		Manifest:  *cm,
		SecretEnv: secretEnv,
	}, 20*time.Second)
	if err != nil {
		_ = os.RemoveAll(paths.SessionDir(id))
		return nil, err
	}
	var updated Manifest
	if json.Unmarshal(raw, &updated) == nil && updated.ID == id {
		cm = &updated
	}
	info := infoFrom(cm, true)
	return &info, nil
}

func loadRunningSandboxPrimary(inID string) (*Manifest, error) {
	if !validID(inID) {
		return nil, UserError("not_found", "no session "+inID, "List sessions with ls.")
	}
	target, err := LoadManifest(inID)
	if err != nil {
		return nil, UserError("not_found", "no session "+inID, "List sessions with ls.")
	}
	primaryID := sandboxRootID(target)
	primary := target
	if primaryID != target.ID {
		primary, err = LoadManifest(primaryID)
		if err != nil {
			return nil, UserError("not_found", "primary sandbox session "+primaryID+" is gone", "List sessions with ls.")
		}
	}
	if !isWalled(primary) {
		return nil, UserError("not_walled", primary.Name+" has no sandbox wall", "Use new <host> --dir to start another session in the same folder.")
	}
	if !alive(primaryID) {
		return nil, UserError("sandbox_stopped", "sandbox "+primary.Name+" is stopped", "Wake it first with resume, then try again.")
	}
	return primary, nil
}

func buildChildEnv(a NewArgs, agent agents.Agent, inline bool) ([]string, string) {
	term := a.Term
	if !HasTerminfo(term) {
		term = "xterm-256color"
	}
	colorterm := a.ColorTerm
	if colorterm == "" {
		colorterm = "truecolor"
	}
	childEnv := []string{
		"TERM=" + term,
		"COLORTERM=" + colorterm,
	}
	if a.TermProgram != "" {
		childEnv = append(childEnv, "TERM_PROGRAM="+a.TermProgram)
	}
	if a.TermProgramVersion != "" {
		childEnv = append(childEnv, "TERM_PROGRAM_VERSION="+a.TermProgramVersion)
	}
	if inline {
		for k, v := range agent.Inline.Env {
			if v != "" {
				childEnv = append(childEnv, k+"="+v)
			}
		}
	}
	for k, v := range agent.ProxyEnv {
		if v != "" {
			childEnv = append(childEnv, k+"="+v)
		}
	}
	return childEnv, term
}

func encodeSecretEnv(secrets map[string]api.Secret) []string {
	if len(secrets) == 0 {
		return nil
	}
	out := make([]string, 0, len(secrets))
	for k, v := range secrets {
		out = append(out, k+"="+string(v))
	}
	return out
}

func defaultChildName(primaryName, primaryID, kind string) string {
	used := map[string]bool{primaryName: true}
	for _, c := range sandboxChildren(primaryID) {
		used[c.Name] = true
	}
	suffix := kind
	switch kind {
	case "shell":
		suffix = "sh"
	case "":
		suffix = "run"
	}
	base := primaryName + "-" + suffix
	if !used[base] {
		return base
	}
	for n := 2; n < 1000; n++ {
		cand := base + strconv.Itoa(n)
		if !used[cand] {
			return cand
		}
	}
	return base + "-" + NewID()[:4]
}

// stopSandbox stops a walled sandbox (whether called on its primary or any
// child session), marks every session in the sandbox stopped, and returns
// StopResult with Sessions listing every stopped session (spec 6.11).
func stopSandbox(requestedID string, m *Manifest) (*StopResult, error) {
	primaryID := sandboxRootID(m)
	primary := m
	if primaryID != requestedID {
		var err error
		primary, err = LoadManifest(primaryID)
		if err != nil {
			primary = m
			primaryID = requestedID
		}
	}
	children := sandboxChildren(primaryID)

	running := alive(primaryID)
	if !running && primary.Status.State == StateStopped {
		if primary.Run.Unit != "" {
			stopUnit(primary.Run.Unit)
			primary.Run.Unit = ""
			_ = primary.Save()
		}
		all := make([]SessionInfo, 0, 1+len(children))
		all = append(all, infoFrom(primary, false))
		reqInfo := infoFrom(primary, false)
		for _, c := range children {
			ci := infoFrom(c, alive(c.ID))
			all = append(all, ci)
			if c.ID == requestedID {
				reqInfo = ci
			}
		}
		return &StopResult{Session: reqInfo, AlreadyStopped: true, Sessions: all}, nil
	}

	stoppedBySup := stopWalledHolder(primaryID, primary)
	if reloaded, err := LoadManifest(primaryID); err == nil {
		primary = reloaded
	}
	now := time.Now().UTC()
	markStopped := func(sm *Manifest) error {
		sm.Run.HolderPID = 0
		sm.Run.AgentPID = 0
		sm.Run.Unit = ""
		sm.Run.RespawnCount = 0
		sm.Guest = nil
		sm.DebugUntil = nil
		sm.Status = Status{
			State:  StateStopped,
			Since:  now,
			Source: "user",
			Reason: "stopped by request",
		}
		return sm.Save()
	}
	if err := markStopped(primary); err != nil {
		return nil, fmt.Errorf("save the manifest of session %s: %w", primary.ID, err)
	}
	if !stoppedBySup {
		RecordWatchEvent(api.WatchEvent{
			Event:   api.EventSandboxStopped,
			Session: primary.ID,
			Name:    primary.Name,
			Agent:   primary.Agent.Kind,
			State:   StateStopped,
			Reason:  "stop",
		})
	}
	all := make([]SessionInfo, 0, 1+len(children))
	pInfo := infoFrom(primary, false)
	all = append(all, pInfo)
	reqInfo := pInfo
	for _, c := range children {
		if reloaded, err := LoadManifest(c.ID); err == nil {
			c = reloaded
		}
		_ = markStopped(c)
		ci := infoFrom(c, false)
		all = append(all, ci)
		if c.ID == requestedID {
			reqInfo = ci
		}
	}
	return &StopResult{Session: reqInfo, Sessions: all}, nil
}

// wakeSandboxChildren respawns persisted child sessions of primaryID inside
// its freshly started supervisor (isolation spec 6.11). Agents resume with
// resumeArgv, custom commands run again, and shells start fresh.
func wakeSandboxChildren(primaryID string) {
	children := sandboxChildren(primaryID)
	for _, child := range children {
		if alive(child.ID) {
			continue
		}
		if child.Status.State == StateExited && child.Status.Reason == "killed by request" {
			continue
		}
		argv, why, err := resumeArgv(child)
		if err != nil {
			warnSession(child.ID, "child session not woken: "+err.Error())
			continue
		}
		dropUnusableID(child)
		child.Agent.Argv = argv
		child.Run.BootID = BootID()
		child.Guest = nil
		child.Status = Status{State: StateStarting, Since: time.Now().UTC(), Reason: "woken with sandbox: " + why}
		_ = child.Save()

		if raw, err := HolderRequest(primaryID, "spawn", SpawnSocketRequest{Manifest: *child}, 15*time.Second); err != nil {
			warnSession(child.ID, "failed to wake child session in sandbox: "+err.Error())
		} else {
			var updated Manifest
			if json.Unmarshal(raw, &updated) == nil && updated.ID == child.ID {
				_ = updated.Save()
			}
		}
	}
}

// deleteSandboxChildren removes all child session directories belonging to
// primaryID (after the primary supervisor has already been stopped) and returns
// their SessionInfo summaries.
func deleteSandboxChildren(primaryID string) []SessionInfo {
	children := sandboxChildren(primaryID)
	out := make([]SessionInfo, 0, len(children))
	for _, c := range children {
		out = append(out, infoFrom(c, false))
		_ = os.Remove(paths.Socket(c.ID))
		_ = os.RemoveAll(paths.SessionDir(c.ID))
	}
	return out
}

// resumeChildInSandbox resumes a child session. If the sandbox is stopped, it
// wakes the primary sandbox (which wakes all children including id); if the
// sandbox is running, it respawns just this child inside the running sandbox.
func resumeChildInSandbox(id string, child *Manifest, a api.ResumeArgs) (*ResumeResult, error) {
	primaryID := child.SandboxOf
	if !alive(primaryID) {
		if _, err := Resume(api.ResumeArgs{ID: primaryID, MemoryMax: a.MemoryMax, Secrets: a.Secrets}); err != nil {
			return nil, err
		}
		reloaded, err := LoadManifest(id)
		if err != nil {
			return nil, err
		}
		return &ResumeResult{Session: infoFrom(reloaded, alive(id)), Why: "woke with its sandbox"}, nil
	}
	if alive(id) && child.Status.State != StateExited && child.Status.State != StateErrored {
		return nil, UserError("agent_running", "the agent of "+child.Name+" still runs",
			"Attach to it. To start it again, quit the agent first, then resume the session.")
	}
	secrets, err := resumeSecrets(child.Secrets, a.Secrets)
	if err != nil {
		return nil, err
	}
	argv, why, err := resumeArgv(child)
	if err != nil {
		return nil, err
	}
	stopHolder(id, child)
	if reloaded, rerr := LoadManifest(id); rerr == nil {
		child = reloaded
	}
	dropUnusableID(child)
	child.Agent.Argv = argv
	child.Run.BootID = BootID()
	child.Guest = nil
	child.Status = Status{State: StateStarting, Since: time.Now().UTC(), Reason: "started again by a resume: " + why}
	if err := child.Save(); err != nil {
		return nil, err
	}
	var secretEnv []string
	for k, v := range secrets {
		secretEnv = append(secretEnv, k+"="+string(v))
	}
	defer clear(secretEnv)
	raw, err := HolderRequest(primaryID, "spawn", SpawnSocketRequest{
		Manifest:  *child,
		SecretEnv: secretEnv,
	}, 20*time.Second)
	if err != nil {
		return nil, UserError("start_failed", err.Error(), "Check the sandbox status with ls.")
	}
	var updated Manifest
	if json.Unmarshal(raw, &updated) == nil && updated.ID == id {
		child = &updated
	}
	return &ResumeResult{Session: infoFrom(child, alive(id)), Why: why}, nil
}
