package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
)

func newUnwalled(ctx context.Context, a NewArgs) (_ *SessionInfo, err error) {
	return newUnwalledModeV1(ctx, a, false)
}

// The legacy mode is reached only through the existing explicit test-driver
// seam. Production None always selects its real supervisor/native path.
func newUnwalledModeV1(ctx context.Context, a NewArgs, legacyFixture bool) (_ *SessionInfo, err error) {
	if a.NamedSSHKeys != nil {
		return nil, UserError("ssh_key_kind", "named SSH keys need a sandbox agent", "Choose a walled session for named SSH keys. The laptop agent has a separate own-host option.")
	}
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
	intent, _, err := PrepareSecretDelivery(a.SecretDelivery, a.Secrets, false, nil)
	if err != nil {
		return nil, err
	}
	if intent != nil && len(intent.TrustedUses) != 0 {
		return nil, UserError("not_walled", "trusted and bound secret roles cannot enter an unwalled session", "Choose the original admitted sandbox wall for those secret roles.")
	}
	login, err := LoginEnv(15 * time.Second)
	if err != nil {
		login = os.Environ()
	}
	id := a.ID
	if id == "" {
		id = unusedID()
	}
	if !validID(id) {
		return nil, UserError("bad_request", "the requested session ID is invalid", "Prepare a fresh session ID through the current client.")
	}
	// Find the agent before anything is asked or made. A missing agent
	// leaves nothing behind: no folder, no worktree and no branch.
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	env, term := sessionEnv(a, id, login, agent, inline)
	if a.Environment != nil {
		env, term, err = resolvedUnwalledEnv(a, id, login, agent, inline, initialSecretState(a.SecretDelivery), secrets)
		if err != nil {
			return nil, err
		}
	}
	env, err = withSSHAgentSocket(env, id, a.SSHAgent)
	if err != nil {
		return nil, err
	}
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
	if !legacyFixture {
		m.Wall = &WallRecord{Level: "none", Driver: "none", Version: version.Version, Reason: "runs as the host account without a wall", At: now, Floor: a.Floor, DowngradedFrom: a.DowngradedFrom}
		m.SecretState = initialSecretState(intent)
	}
	if err := applyPlacementAuditNewV1(m, a.Placement); err != nil {
		return nil, err
	}
	m.Git = gi
	m.HostLabel, m.Task, m.SSHAgent = a.HostLabel, a.Task, a.SSHAgent
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
	return startSessionContext(ctx, m, a.Secrets, strings.TrimSpace(a.Name) == "")
}
