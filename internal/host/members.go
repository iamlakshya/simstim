package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
)

// SandboxPrimaryID reads only trusted manifests. A member has exactly one
// primary reference; arbitrary chains, self-references and unwalled rows refuse.
func SandboxPrimaryID(id string) (string, error) {
	if !api.ValidSessionID(id) {
		return "", UserError("not_found", "no such session", "List sessions to find its address.")
	}
	m, err := LoadManifest(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		return "", err
	}
	if m.ID != id {
		return "", memberError("the protected session identity is inconsistent")
	}
	if m.SandboxOf == "" {
		return id, nil
	}
	if !api.ValidSessionID(m.SandboxOf) || m.SandboxOf == id {
		return "", memberError("the session has an invalid sandbox owner")
	}
	p, err := LoadManifest(m.SandboxOf)
	if err != nil {
		return "", err
	}
	if p.ID != m.SandboxOf || p.SandboxOf != "" || p.Wall == nil {
		return "", memberError("the session's sandbox owner is invalid")
	}
	return p.ID, nil
}

// SandboxMembers lists saved child commands in creation order. Child
// manifests cannot nominate another primary during this traversal.
func SandboxMembers(primary string) ([]*Manifest, error) {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		return nil, err
	}
	var out []*Manifest
	for _, entry := range entries {
		if !entry.IsDir() || !api.ValidSessionID(entry.Name()) {
			continue
		}
		m, err := LoadManifest(entry.Name())
		if err != nil {
			continue
		}
		if m.SandboxOf == primary && m.ID != primary {
			if m.ID != entry.Name() {
				return nil, memberError("a protected member identity is inconsistent")
			}
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *Manifest) int {
		if n := a.CreatedAt.Compare(b.CreatedAt); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// MemberStart owns a fresh host manifest directory until the supervisor sends
// its spawn. Finish leaves launched ownership intact even when its reply is lost.
type MemberStart struct {
	Manifest      *Manifest
	reservation   *sessionReservation
	unlock        func()
	unlockPrimary func()
}

// Finish releases the reservation, preserving a possibly launched member.
func (s *MemberStart) Finish(launched bool) {
	s.reservation.launched = launched
	s.reservation.close() // cleanup is part of the same primary transaction
	s.unlock()
	s.unlockPrimary()
}

// PrepareSandboxMember uses the canonical agent command and fixed guest env
// builders. It does not bind another mount, install tree, policy or host env.
func PrepareSandboxMember(parent *Manifest, a api.JoinArgs) (*MemberStart, error) {
	if parent.Wall == nil {
		return nil, memberError("the parent has no sandbox")
	}
	if !api.ValidSessionID(a.Parent) || !api.ValidSessionID(a.ID) {
		return nil, memberError("spawn needs the original parent and a reserved child ID")
	}
	if err := checkName(a.Name); err != nil {
		return nil, err
	}
	args := NewArgs{Agent: a.Agent, Argv: slices.Clone(a.Argv), Name: a.Name, Term: a.Term, ColorTerm: a.ColorTerm, TermProgram: a.TermProgram, TermProgramVersion: a.TermProgramVersion, Cols: a.Cols, Rows: a.Rows, Secrets: a.Secrets, SecretDelivery: a.SecretDelivery}
	argv, kind, agent, known, err := pickAgent(args)
	if err != nil {
		return nil, err
	}
	for i, arg := range argv {
		if arg == "$SHELL" {
			if IsNoneSupervisor(parent) {
				argv[i] = LoginShell()
			} else {
				argv[i] = SandboxShell(parent)
			}
		}
	}
	argv, hooks, sessionUUID := agentArgv(argv, args, agent, known, false)
	if a.SecretDelivery == nil && (parent.SecretState != nil || len(a.Secrets) != 0) {
		return nil, UserError("secret_delivery_required", "a sandbox member needs explicit secret delivery intent", "Use a matching client and freshly approve the requested secret modes.")
	}
	intent, names, err := PrepareSecretDelivery(a.SecretDelivery, a.Secrets, parent.Wall.Level != "none", nil)
	if err != nil {
		return nil, err
	}
	unlockPrimary, err := lockSession(parent.ID)
	if err != nil {
		return nil, err
	}
	if err := CheckSandboxSnapshotLocked(a.Parent, parent.ID, a.Snapshot); err != nil {
		unlockPrimary()
		return nil, err
	}
	reservation, err := reserveSessionID(a.ID)
	if err != nil {
		unlockPrimary()
		return nil, err
	}
	unlock, err := lockSession(a.ID)
	if err != nil {
		reservation.close()
		unlockPrimary()
		return nil, err
	}
	start := &MemberStart{reservation: reservation, unlock: unlock, unlockPrimary: unlockPrimary}
	ok := false
	defer func() {
		if !ok {
			start.Finish(false)
		}
	}()
	// Reuse only its trusted, already projected PATH, never LoginEnv/LookPath.
	dirs := filepath.SplitList(envValue(parent.Env, "PATH"))
	env, term := walledEnv(args, a.ID, dirs, agent, false)
	if IsNoneSupervisor(parent) {
		env, term = sessionEnv(args, a.ID, parent.Env, agent, false)
		if len(argv) > 0 {
			path, err := lookPath(argv[0], envValue(env, "PATH"))
			if err != nil {
				return nil, UserError("agent_missing", "the selected unwalled member command is not installed", "Install the command on this host, then try again.")
			}
			argv[0] = path
		}
	}
	env = slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, "SSH_AUTH_SOCK=") || strings.HasPrefix(kv, "SSH_AGENT_PID=")
	})
	hook := GuestRunDir() + "/hook-" + a.ID + ".sock"
	if IsNoneSupervisor(parent) {
		hook = filepath.Join(paths.SandboxRoot(parent.ID), "hook-"+a.ID+".sock")
	}
	for i, kv := range env {
		if strings.HasPrefix(kv, brand.Env("HOOK_SOCK")+"=") {
			env[i] = brand.Env("HOOK_SOCK") + "=" + hook
		}
	}
	m := *parent
	m.AutomaticRestore = nil
	m.Inputs, m.CopiesPending = nil, false
	m.extra = nil // A new child never inherits unknown parent authority fields.
	m.VMNeedV1 = nil
	m.SSHAgent = false
	m.ID, m.SandboxOf, m.Name = a.ID, parent.ID, strings.TrimSpace(a.Name)
	m.Task = a.Task
	m.CreatedAt, m.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	m.HostVersion = version.Version
	m.Agent = AgentInfo{Kind: kind, Argv: argv, Hooks: hooks}
	if len(a.Argv) > 0 {
		m.Agent.Command = slices.Clone(argv)
	}
	if known {
		m.Agent.ResumeArgv = slices.Clone(agent.Resume)
		m.Agent.ContinueArgv = slices.Clone(agent.Continue)
		if agent.IDCapture == "choose" && passesID(argv, sessionUUID) {
			m.Agent.AgentSessionID = sessionUUID
			m.Agent.IDSource = "chosen"
		}
	}
	m.Env, m.Term = env, TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows}
	m.Secrets, m.MissingSecrets = names, nil
	m.SecretState = initialSecretState(intent)
	m.Run = Runtime{Socket: paths.Socket(a.ID), BootID: BootID()}
	m.Status = Status{State: StateStarting, Since: m.CreatedAt}
	m.Guest, m.Life = nil, nil
	m.SetupHook, m.ResumeHook, m.RunHook, m.SetupHash = "", "", "", ""
	m.Git = nil
	m.DebugUntil = nil
	start.Manifest = &m
	applySessionIdentity(&m)
	if err := m.Save(); err != nil {
		return nil, err
	}
	if err := start.recordCreatedMemberRootV1(context.Background(), parent); err != nil {
		return nil, err
	}
	ok = true
	return start, nil
}

// MemberInfo uses the canonical row conversion, including sandbox_of.
func MemberInfo(m *Manifest, alive bool) api.SessionInfo { return infoFrom(m, alive) }

// SandboxShell is the configured single executable inside the guest.
func SandboxShell(m *Manifest) string {
	if m.Policy != nil && m.Policy.Sandbox.Shell != nil && *m.Policy.Sandbox.Shell != "" {
		return *m.Policy.Sandbox.Shell
	}
	return ""
}

func memberError(message string) error {
	return UserError("sandbox_member", message, fmt.Sprintf("List sessions with %s ls, then use a sandbox's address.", brand.Name))
}

// MemberAgentKinds derives needs exclusively from the canonical saved commands.
// A guest's status and agent_id claim cannot add an agent kind.
func MemberAgentKinds(primary *Manifest, members []*Manifest) []string {
	out := []string{}
	for _, m := range append([]*Manifest{primary}, members...) {
		if _, ok := agents.Find(m.Agent.Kind); ok && !slices.Contains(out, m.Agent.Kind) {
			out = append(out, m.Agent.Kind)
		}
	}
	slices.Sort(out)
	return out
}

// ResumeSandboxMember reuses the single canonical resume command policy. No
// repository hook runs on the host, and a shell always starts a fresh prompt.
func ResumeSandboxMember(saved, parent *Manifest) (*Manifest, error) {
	if saved.SandboxOf != parent.ID || saved.ID == parent.ID || !api.ValidSessionID(saved.ID) {
		return nil, memberError("the saved sandbox membership changed")
	}
	if err := ValidateManifestStart(saved); err != nil {
		return nil, err
	}
	m := CloneManifest(*saved)
	var argv []string
	var err error
	if m.Agent.Kind == "shell" && len(m.Agent.Command) == 0 {
		argv = slices.Clone(m.Agent.Argv)
		if len(argv) == 0 {
			return nil, memberError("the saved shell command is empty")
		}
		if IsNoneSupervisor(parent) {
			argv[0] = LoginShell()
		} else {
			argv[0] = SandboxShell(parent)
		}
	} else {
		argv, _, err = resumeArgv(&m)
		if err != nil {
			return nil, err
		}
	}
	m.Agent.Argv = argv
	m.Cwd = parent.Cwd
	m.HostLabel, m.SandboxName = parent.HostLabel, parent.SandboxName
	applySessionIdentity(&m)
	m.Run = Runtime{Socket: paths.Socket(m.ID), BootID: BootID()}
	m.Status = Status{State: StateStarting, Since: time.Now().UTC()}
	m.Guest, m.Life = nil, nil
	m.RunHook = ""
	return &m, nil
}
