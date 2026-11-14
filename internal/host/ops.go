package host

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// UserError is an error with a code and a suggested next step.
func UserError(code, msg, hint string) *proto.Error {
	return &proto.Error{Code: code, Message: msg, Hint: hint}
}

// questionError is a UserError that asks the user something before a start
// can go on. Its data is the question, q. The client asks the user, and
// starts again with the answer.
func questionError(code, msg, hint string, q any) *proto.Error {
	e := UserError(code, msg, hint)
	e.Data, _ = json.Marshal(q)
	return e
}

// NewArgs describes a session to start.
type NewArgs = api.NewArgs

// SessionInfo is one row of the session list.
type SessionInfo = api.SessionInfo

// NewID makes a short random session ID: 8 characters of the ID's
// alphabet, at least one of them a digit. A name without a digit then never
// looks like an ID (cli-spec 9.2). An ID drawn without one gets a random
// digit at a random place.
func NewID() string { return api.NewSessionID() }

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func expandHome(p string) string {
	if p == "" || p == "~" {
		h, _ := os.UserHomeDir()
		return h
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, rest)
	}
	return p
}

// New starts a session and returns its row.
func New(a NewArgs) (_ *SessionInfo, err error) {
	if err := checkName(a.Name); err != nil {
		return nil, err
	}
	if err := checkMemoryMax(a.MemoryMax); err != nil {
		return nil, err
	}
	argv, kind, agent, known, err := pickAgent(a)
	if err != nil {
		return nil, err
	}
	cwd := expandHome(a.Cwd)
	missing, err := checkFolder(cwd, a.Worktree)
	if err != nil {
		return nil, err
	}
	secrets, err := checkSecrets(a.Secrets)
	if err != nil {
		return nil, err
	}
	login, err := LoginEnv(15 * time.Second)
	if err != nil {
		login = os.Environ()
	}
	id := unusedID()
	// Find the agent before anything is asked or made. A missing agent
	// leaves nothing behind: no folder, no worktree and no branch.
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	env, term := sessionEnv(a, id, login, agent, inline)
	argv, hooks, sessionUUID := agentArgv(argv, a, agent, known, inline)
	if _, err := lookPath(argv[0], envValue(env, "PATH")); err != nil {
		return nil, UserError("agent_missing", argv[0]+" is not installed on this host", "Install it on the host, then try again.")
	}
	if len(missing) > 0 {
		if !a.MakeFolder {
			return nil, folderQuestion(cwd)
		}
		if err := makeFolders(cwd); err != nil {
			return nil, err
		}
		// A start that fails after this point removes the new folders again,
		// unless it kept its session.
		defer func() { removeFoldersAfter(err, id, missing) }()
	}
	var gi *GitInfo
	if a.Worktree {
		n := a.Name
		if strings.TrimSpace(n) == "" {
			n = filepath.Base(cwd)
		}
		gi, err = makeWorktree(cwd, n, id, a.Base)
		if err != nil {
			return nil, err
		}
		cwd = gi.Worktree
	}
	sessHooks, err := setupHooks(a, cwd, gi)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = folderName(cwd)
	}
	asked := max(a.MemoryMax, 0)
	now := time.Now().UTC()
	m := &Manifest{
		Schema: 1, ID: id, Name: name, CreatedAt: now, HostVersion: version.Version,
		Agent: AgentInfo{Kind: kind, Argv: argv},
		Cwd:   cwd, Env: env,
		Term:    TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:  Limits{MemoryMax: sessionMemoryMax(asked), MemoryAsked: asked},
		Run:     Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:  Status{State: StateStarting, Since: now},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
	}
	m.Git = gi
	m.Secrets = secrets
	m.SetupHook, m.ResumeHook, m.SetupHash = sessHooks.setup, sessHooks.resume, sessHooks.hash
	m.RunHook = m.SetupHook
	if len(a.Argv) > 0 {
		// A restore or a resume runs the user's own command again, unless
		// the agent resumes its own session by ID (resumeArgv).
		m.Agent.Command = slices.Clone(argv)
	}
	if known {
		m.Agent.ResumeArgv = agent.Resume
		m.Agent.ContinueArgv = agent.Continue
		m.Agent.Hooks = hooks
		if inline {
			m.Agent.Inline = agent.Inline.Args
		}
		// The agent gets the ID only through its command. A command of the
		// user's own passes it only with the {session_uuid} placeholder, and
		// a resume by an ID that the agent never got would find nothing.
		if agent.IDCapture == "choose" && passesID(argv, sessionUUID) {
			m.Agent.AgentSessionID = sessionUUID
			m.Agent.IDSource = "chosen"
		}
	}
	return startSession(m, a.Secrets, strings.TrimSpace(a.Name) == "")
}

// pickAgent returns the command a new session runs and its kind, and the
// agent from the table that it runs, if any; known is true when the caller
// named that agent.
func pickAgent(a NewArgs) (argv []string, kind string, agent agents.Agent, known bool, err error) {
	selected, err := agents.ResolveStart(a.Agent, a.Argv)
	if err != nil {
		return nil, "", agents.Agent{}, false, UserError("unknown_agent", err.Error(), "Pick one of the listed agents, or pass a command.")
	}
	return selected.Argv, selected.Kind, selected.Agent, selected.Known, nil
}

// sessionEnv returns the agent's environment: the login environment, the
// terminal's identity, the session's own variables and those of the agent's
// inline mode. It also returns the TERM it chose.
func sessionEnv(a NewArgs, id string, login []string, agent agents.Agent, inline bool) (env []string, term string) {
	term = a.Term
	if !HasTerminfo(term) {
		term = "xterm-256color"
	}
	colorterm := a.ColorTerm
	if colorterm == "" {
		colorterm = "truecolor"
	}
	extra := map[string]string{
		"TERM": term, "COLORTERM": colorterm,
		"TERM_PROGRAM": a.TermProgram, "TERM_PROGRAM_VERSION": a.TermProgramVersion,
		// Only the hook socket: the session socket refuses the session's own
		// programs where the peer check works (holder_peer.go).
		brand.Env("SESSION"): id, brand.Env("HOOK_SOCK"): paths.HookSocket(id),
	}
	if inline {
		for k, v := range agent.Inline.Env {
			extra[k] = v
		}
	}
	return AgentEnv(login, extra), term
}

// passesID reports whether the command argv hands the agent the ID id, in
// one of its arguments.
func passesID(argv []string, id string) bool {
	return slices.ContainsFunc(argv, func(s string) bool { return strings.Contains(s, id) })
}

// agentArgv fills in the agent's command: its variables, the login shell,
// the home folder, its hooks and its inline mode. It returns the command,
// the hooks' arguments, and the session UUID it chose for the agent.
func agentArgv(argv []string, a NewArgs, agent agents.Agent, known, inline bool) (out, hooks []string, sessionUUID string) {
	vars := map[string]string{"session_uuid": newUUID()}
	argv = agents.Expand(argv, vars)
	for i, s := range argv {
		if s == "$SHELL" {
			argv[i] = LoginShell()
		}
	}
	if strings.HasPrefix(argv[0], "~/") {
		argv[0] = expandHome(argv[0])
	}
	if known && len(agent.Hooks) > 0 && len(a.Argv) == 0 {
		hooks = hookArgs(agent.Hooks)
		argv = append(argv, hooks...)
	}
	if inline {
		argv = append(argv, agent.Inline.Args...)
	}
	return argv, hooks, vars["session_uuid"]
}

// makeSessionDir creates the new session's private folder, and fails if a
// session already has that ID.
func makeSessionDir(id string) error {
	if err := paths.EnsurePrivateDir(paths.SessionsDir()); err != nil {
		return err
	}
	dir := paths.SessionDir(id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("make %s: %w", dir, err)
	}
	//nolint:gosec // G302: a folder needs the execute bit to be opened; 0700 is the owner's alone
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.Remove(dir)
		return fmt.Errorf("make %s private: %w", dir, err)
	}
	return nil
}

// debugUntil is when the session's debug stops by itself, while it is on.
func debugUntil(m *Manifest, alive bool) *time.Time {
	if !alive || m.DebugUntil == nil || !m.DebugUntil.After(time.Now()) {
		return nil
	}
	return m.DebugUntil
}

func alive(id string) bool {
	d := net.Dialer{Timeout: 300 * time.Millisecond}
	c, err := d.DialContext(context.Background(), "unix", paths.Socket(id))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// List returns every session on this host, newest first.
func List() ([]SessionInfo, error) {
	entries, err := os.ReadDir(paths.SessionsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SessionInfo{}, nil
		}
		return nil, fmt.Errorf("list the sessions: %w", err)
	}
	out := make([]SessionInfo, 0, len(entries))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := LoadManifest(id)
			if err != nil {
				return
			}
			reconcileForList(id, m)
			info := infoFrom(m, alive(id))
			mu.Lock()
			out = append(out, info)
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}
