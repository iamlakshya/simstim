package host

import (
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

// isWalled reports whether m runs under a supervisor and guest init.
func isWalled(m *Manifest) bool { return m != nil && m.Wall != nil }

// startSessionProc starts either the supervisor (for walled sessions) or the
// holder (for unwalled sessions).
func startSessionProc(m *Manifest) error {
	if isWalled(m) {
		return launchSupervisor(m)
	}
	return startHolder(m)
}

// fillWallInfo copies the trusted wall record and claimed flag onto a session row.
func fillWallInfo(info *SessionInfo, m *Manifest) {
	if m.Wall != nil {
		info.Wall = &api.WallInfo{
			Level:          m.Wall.Level,
			Driver:         m.Wall.Driver,
			Version:        m.Wall.Version,
			Floor:          m.Wall.Floor,
			DowngradedFrom: m.Wall.DowngradedFrom,
			Preset:         m.Wall.Preset,
		}
	}
	info.Claimed = m.Status.Claimed && info.Alive && info.State != StateLost
	if info.Alive && len(m.GrantedPorts) > 0 {
		info.Ports = slices.Clone(m.GrantedPorts)
	}
}

func fillAgentManifest(m *Manifest, userArgv, argv []string, agent agents.Agent, known, inline bool, hooks []string, sessionUUID string) {
	if len(userArgv) > 0 {
		// A restore or a resume runs the user's own command again, unless
		// the agent resumes its own session by ID (resumeArgv).
		m.Agent.Command = slices.Clone(argv)
	}
	if !known {
		return
	}
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

// newWalled creates a session that runs under a supervisor and guest init.
// Its environment comes from the fixed base of spec 4.2, never from LoginEnv.
func newWalled(a NewArgs) (_ *SessionInfo, err error) {
	switch a.Wall {
	case "process":
		return newProcessWalled(a)
	case "plain":
		return newPlainWalled(a)
	default:
		return nil, UserError("wall_unavailable", fmt.Sprintf("wall %q is not available in this build", a.Wall),
			"Use --wall process for a bubblewrap sandbox, or --wall none for an unwalled session.")
	}
}

func newPlainWalled(a NewArgs) (_ *SessionInfo, err error) {
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
	id := NewID()
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	argv, hooks, sessionUUID := agentArgv(argv, a, agent, known, inline)

	// Only PATH from the login shell is consulted to resolve the agent binary
	// (spec 7.1). No other variable from LoginEnv enters the walled session.
	searchPath := walledSearchPath()
	resolved, err := lookPath(argv[0], searchPath)
	if err != nil {
		return nil, UserError("agent_missing", argv[0]+" is not installed on this host", "Install it on the host, then try again.")
	}
	if len(missing) > 0 {
		if !a.MakeFolder {
			return nil, folderQuestion(cwd)
		}
		if err := makeFolders(cwd); err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				removeFolders(missing)
			}
		}()
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
	guestHome := filepath.Join(paths.SessionDir(id), "home")
	if err := paths.EnsurePrivateDir(guestHome); err != nil {
		return nil, err
	}
	env, term := walledEnv(a, id, guestHome, resolved, agent, inline)
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = filepath.Base(cwd)
	}
	asked := max(a.MemoryMax, 0)
	now := time.Now().UTC()
	m := &Manifest{
		Schema: 1, ID: id, Name: name, CreatedAt: now, HostVersion: version.Version,
		Agent:   AgentInfo{Kind: kind, Argv: argv},
		Cwd:     cwd,
		Env:     env,
		Term:    TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:  Limits{MemoryMax: sessionMemoryMax(asked), MemoryAsked: asked},
		Run:     Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:  Status{State: StateStarting, Since: now},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Wall: &WallRecord{
			Level:   "none",
			Driver:  "plain",
			Version: version.Version,
			At:      now,
		},
	}
	m.Git = gi
	m.Secrets = secrets
	m.SetupHook, m.ResumeHook, m.SetupHash = sessHooks.setup, sessHooks.resume, sessHooks.hash
	m.RunHook = m.SetupHook
	fillAgentManifest(m, a.Argv, argv, agent, known, inline, hooks, sessionUUID)
	return startSession(m, a.Secrets)
}

// walledSearchPath returns only the PATH value used on the trusted side to
// locate the agent binary, never passing the rest of LoginEnv to the session.
func walledSearchPath() string {
	if login, err := LoginEnv(15 * time.Second); err == nil {
		if p := envValue(login, "PATH"); p != "" {
			return p
		}
	}
	return os.Getenv("PATH")
}

// walledEnv constructs the agent's environment from the fixed base of spec 4.2,
// never from LoginEnv.
func walledEnv(a NewArgs, id, guestHome, resolvedBin string, agent agents.Agent, inline bool) (env []string, term string) {
	term = a.Term
	if !HasTerminfo(term) {
		term = "xterm-256color"
	}
	colorterm := a.ColorTerm
	if colorterm == "" {
		colorterm = "truecolor"
	}
	pathDirs := []string{"/usr/local/bin", "/usr/bin", "/bin"}
	if binDir := filepath.Dir(resolvedBin); binDir != "" && binDir != "." && !slices.Contains(pathDirs, binDir) {
		pathDirs = append([]string{binDir}, pathDirs...)
	}
	// Fixed base from spec 4.2: HOME, USER, LOGNAME, LANG, PATH, terminal vars,
	// session ID, and optional agent inline vars. Guest init sets HOOK_SOCK.
	env = []string{
		"HOME=" + guestHome,
		"USER=agent",
		"LOGNAME=agent",
		"LANG=C.UTF-8",
		"PATH=" + strings.Join(pathDirs, string(os.PathListSeparator)),
		"TERM=" + term,
		"COLORTERM=" + colorterm,
		brand.Env("SESSION") + "=" + id,
	}
	if a.TermProgram != "" {
		env = append(env, "TERM_PROGRAM="+a.TermProgram)
	}
	if a.TermProgramVersion != "" {
		env = append(env, "TERM_PROGRAM_VERSION="+a.TermProgramVersion)
	}
	if inline {
		for k, v := range agent.Inline.Env {
			if v != "" {
				env = append(env, k+"="+v)
			}
		}
	}
	return env, term
}
