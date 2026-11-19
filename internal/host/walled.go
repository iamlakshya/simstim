package host

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/files"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// A walled session runs with the split of spec 4. Its supervisor runs in
// the session's unit, outside the wall, and serves the session's socket.
// The guest init, the holder and the agent run inside the wall. One link
// joins them. The bridge still dials paths.Socket(id), so the client's path
// does not change.

// WallDriver names the driver that builds a sandbox for each new session,
// when it is set. Only tests set it, to "plain": the one driver of this
// version. It starts the guest init as a plain child of the supervisor,
// and builds no wall (spec 5.1).
var WallDriver string

// newWalled starts a walled session behind the driver, and returns its
// row. It checks the arguments as New does. The agent's environment comes
// from a fixed base, never from the login environment (spec 4.2). The
// login environment only finds the agent's binary, which the guest then
// runs by its full path.
func newWalled(ctx context.Context, a NewArgs, driver string, original NewArgs) (_ *SessionInfo, err error) {
	microVM := driver == "chv" || driver == "firecracker"
	if microVM && len(a.Mounts) > 0 {
		return nil, UserError("wall_mount", "host folder mounts cannot enter a microvm", "Use declared laptop inputs or copy files into its workspace.")
	}
	intent, secrets, err := PrepareSecretDelivery(a.SecretDelivery, a.Secrets, driver != "plain", nil)
	if err != nil {
		return nil, err
	}
	if intent == nil && len(a.Secrets) != 0 {
		return nil, UserError("secret_unbound", "sandbox secrets need explicit delivery intent", "Update the client and choose bound delivery or approve exposure by name.")
	}
	policy, layers, err := egress.MergeLayers(a.Layers)
	if err != nil {
		return nil, err
	}

	if err := checkMemoryMax(a.MemoryMax); err != nil {
		return nil, err
	}
	if a.Worktree {
		return nil, UserError("wall_worktree", "a session in a sandbox cannot have a worktree",
			"Start it without --worktree.")
	}
	argv, kind, agent, known, err := pickAgent(a)
	if err != nil {
		return nil, err
	}
	laptopPrimary := a.Inputs != nil && len(a.Inputs.Repositories) > 0 && a.Cwd == ""
	cwd := ""
	var missing []string
	if !laptopPrimary {
		cwd = expandHome(a.Cwd)
		missing, err = checkFolder(cwd, false)
		if err != nil {
			return nil, err
		}
	}
	// The login environment only finds the binary, as New does. It never
	// reaches the guest.
	var login []string
	if !microVM {
		login, err = LoginEnv(15 * time.Second)
		if err != nil {
			return nil, UserError("agent_missing", "cannot resolve the current host executable environment", "Check the host login environment, then try again.")
		}
	}
	reservation, err := reserveSessionID(a.ID)
	if err != nil {
		return nil, err
	}
	operation, err := beginReservedSessionOperation(ctx, reservation)
	if err != nil {
		reservation.close()
		return nil, err
	}
	defer func() { reservation.close(); operation.Close() }()
	id := reservation.id
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	if microVM {
		argv = vmCommand(argv)
	}
	argv, err = SandboxShellCommand(a, policy, argv)
	if err != nil {
		return nil, err
	}
	argv, hooks, sessionUUID := agentArgv(argv, a, agent, known, inline)
	bin := argv[0]
	pathDirs := []string{"/opt/agents/bin"}
	if !microVM {
		bin, err = lookPath(argv[0], envValue(login, "PATH"))
		if err != nil {
			return nil, UserError("agent_missing", argv[0]+" is not installed on this host", "Install it on the host, then try again.")
		}
		bin, err = filepath.EvalSymlinks(bin)
		if err != nil {
			return nil, UserError("agent_missing", "cannot resolve the agent executable", "Reinstall the agent on this host, then try again.")
		}
		argv[0] = bin
		pathDirs = agentDirs(bin, envValue(login, "PATH"))
	}
	var filePlan *FilePlan
	if microVM {
		state, e := allAgentState()
		if e != nil {
			return nil, e
		}
		filePlan = &FilePlan{State: state}
	}
	if driver == "process" || driver == "gvisor" {
		home, e := os.UserHomeDir()
		if e != nil {
			return nil, UserError("wall_files", "cannot find the host home", "Check the host account and try again.")
		}
		installs, dirs, e := resolveAgentInstalls(bin, envValue(login, "PATH"), home)
		if e != nil {
			return nil, e
		}
		pathDirs = dirs
		state, e := allAgentState()
		if e != nil {
			return nil, UserError("wall_files", "the agent state data is invalid", "Update the host helper.")
		}
		filePlan = &FilePlan{Installs: installs, State: state}
		for _, value := range a.Mounts {
			mount, e := files.ParseMount(value, home)
			if e != nil {
				return nil, e
			}
			mount, e = files.ResolveGrant(mount)
			if e != nil {
				return nil, asUserError(e, "bad_mount", "Choose a folder within the mount's containing directory.")
			}
			filePlan.Mounts = append(filePlan.Mounts, mount)
		}
		if e := files.CheckMounts(filePlan.Mounts, []string{paths.StateDir(), filepath.Dir(paths.SandboxRoot(id))}); e != nil {
			return nil, asUserError(e, "bad_mount", "Choose a folder outside the private session store.")
		}
	}
	env, term, err := resolvedWalledEnv(a, id, pathDirs, agent, inline, initialSecretState(intent), secrets)
	if err != nil {
		return nil, err
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
	name := strings.TrimSpace(a.Name)
	if name == "" {
		if laptopPrimary {
			name = a.Inputs.Repositories[0].Name
		} else {
			name = filepath.Base(cwd)
		}
	}
	limits, err := newWalledMemoryV1(a, driver, policy.Sandbox, agent)
	if err != nil {
		return nil, err
	}
	asked := limits.MemoryAsked
	now := time.Now().UTC()
	m := &Manifest{
		Schema: 1, ID: id, Name: name, Task: a.Task, CreatedAt: now, HostVersion: version.Version,
		Agent:   AgentInfo{Kind: kind, Argv: argv},
		Cwd:     cwd,
		Env:     env,
		Term:    TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:  limits,
		Run:     Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:  Status{State: StateStarting, Since: now},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Wall:    &WallRecord{Level: "none", Driver: driver, Version: version.Version, Reason: "the test driver builds no wall", At: now},
		Secrets: secrets,
		Policy:  &policy, Layers: layers,
	}
	if err := applyPlacementAuditNewV1(m, original.Placement); err != nil {
		return nil, err
	}
	m.HostLabel = a.HostLabel
	m.Wall.Floor, m.Wall.DowngradedFrom = a.Floor, a.DowngradedFrom
	if microVM {
		m.Wall.Level = "microvm"
		m.Wall.Reason = "runs in its own guest kernel"
	}
	m.SecretState = initialSecretState(intent)
	m.Run.StartEnvelope = intent != nil
	if len(a.Argv) > 0 {
		m.Agent.Command = slices.Clone(argv)
	}
	if known {
		m.Agent.ResumeArgv = agent.Resume
		m.Agent.ContinueArgv = agent.Continue
		m.Agent.Hooks = hooks
		if inline {
			m.Agent.Inline = agent.Inline.Args
		}
		if agent.IDCapture == "choose" && passesID(argv, sessionUUID) {
			m.Agent.AgentSessionID = sessionUUID
			m.Agent.IDSource = "chosen"
		}
	}
	if asked > 0 && m.Limits.MemoryMax != asked {
		return nil, UserError("wall_unavailable", "the requested sandbox memory no longer fits the host cap", "Choose an admitted memory size before starting the sandbox.")
	}
	if driver == "process" || driver == "gvisor" {
		m.Wall.Level = driver
		m.Wall.Reason = "shares the host kernel"
	}
	if err := admitReservedRuntime(ctx, operation, m, original); err != nil {
		return nil, err
	}
	if err := ValidateManifestStart(m); err != nil {
		return nil, err
	}
	if driver == "process" || driver == "gvisor" || microVM {
		var seed Seed
		var e error
		if !laptopPrimary {
			seed, e = createSeed(a, id, name, cwd)
		}
		if e != nil {
			return nil, asUserError(e, "seed_git", "Check the source repository and the host's free space, then try again.")
		}
		filePlan.Seed = seed
		m.Files = filePlan
		m.Cwd = "/work/" + seed.Repo
	}
	if err := addLaptopRepositories(m, a.Inputs, !laptopPrimary && m.Files != nil && m.Files.Seed.Repo != ""); err != nil {
		return nil, err
	}
	if driver == "process" || driver == "gvisor" {
		probeContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		p := process.Probe(probeContext)
		if driver == "gvisor" {
			registered := false
			for _, candidate := range wall.Drivers() {
				if candidate.Name() == driver {
					p, err = probeReservedGvisorV1(probeContext, operation)
					registered = true
					break
				}
			}
			if err != nil {
				cancel()
				return nil, err
			}
			if !registered {
				cancel()
				return nil, UserError("wall_unavailable", "the gvisor driver is unavailable", "Use a helper with the complete registered gvisor driver.")
			}
		}
		cancel()
		if !p.OK {
			return nil, UserError("wall_setup", p.Reason, brand.Name+" host prepare <host>")
		}
	}
	if microVM {
		probe, err := CapabilityProbe(ctx, "")
		if err != nil {
			return nil, err
		}
		available := false
		for _, candidate := range probe.Walls {
			if candidate.Driver == driver && candidate.Level == "microvm" && (candidate.OK || candidate.CanPrepare) {
				available = true
				break
			}
		}
		if !available {
			return nil, UserError("wall_unavailable", "the original selected microvm driver is not currently runnable", "Run the host doctor and prepare this exact selected driver before starting again.")
		}
	}
	if driver == "process" || driver == "gvisor" || microVM {
		if err := seedConnectionAlive(); err != nil {
			return nil, err
		}
	}
	return startReservedSessionWithOperation(m, a.Secrets, reservation, operation)
}
