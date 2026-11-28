package host

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/chv"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// probeMicroVMWall checks whether this host can boot Cloud Hypervisor microvms.
// Tests replace it.
var probeMicroVMWall = func(ctx context.Context) (ok bool, ver, reason, fix string) {
	p := chv.Driver{}.Probe(ctx)
	return p.OK, p.Version, p.Reason, p.Fix
}

// isMicroVM reports whether m uses the Cloud Hypervisor microvm driver.
func isMicroVM(m *Manifest) bool {
	return m != nil && m.Wall != nil && m.Wall.Driver == chv.DriverName
}

// waitStartTimeout returns how long New or startAgain waits for the agent to
// report started after the supervisor socket opens. Microvm cold boots take
// longer than process walls (spec 20.3: 60s boot timeout).
func waitStartTimeout(m *Manifest) time.Duration {
	if isMicroVM(m) {
		return chv.DefaultBootTimeout
	}
	return 5 * time.Second
}

func newMicroVMWalled(a NewArgs) (_ *SessionInfo, err error) {
	pctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	ok, chvVer, reason, fix := probeMicroVMWall(pctx)
	cancel()
	if !ok {
		if fix == "" {
			fix = brand.Name + " host show <host>"
		}
		return nil, UserError("wall_unavailable", reason, fix)
	}
	if err := checkNewSessionArgs(a); err != nil {
		return nil, err
	}
	if a.DiskMax > 0 && a.DiskMax < chv.MinSessionDiskBytes {
		return nil, UserError("bad_disk_max",
			fmt.Sprintf("microvm session disk cap %d bytes is below the 16 MiB minimum", a.DiskMax),
			"Set disk_max to 16M or higher.")
	}
	ws, err := checkWalledSettings(a)
	if err != nil {
		return nil, err
	}
	cacheDirs, err := resolveMicroVMCaches(a.KeepCaches, a.Caches)
	if err != nil {
		return nil, err
	}

	argv, kind, agent, known, err := pickAgent(a)
	if err != nil {
		return nil, err
	}
	secrets, err := checkSecrets(a.Secrets)
	if err != nil {
		return nil, err
	}
	boundSecrets, exposedSecrets, err := resolveProcessSecrets(agent, known, secrets, a.SecretBinds, a.ExposedSecrets)
	if err != nil {
		return nil, err
	}
	subscriptionAuth := detectSubscriptionAuth(agent, known, boundSecrets, exposedSecrets)

	imgRec, err := resolveMicroVMImage(a.Image)
	if err != nil {
		return nil, err
	}
	bundle, err := chv.ResolveRuntimeBundle("")
	if err != nil {
		return nil, UserError("runtime_missing", err.Error(), brand.Name+" host prepare <host>")
	}

	id := NewID()
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	hookBin := filepath.Join("/run", brand.Name, "tools", "init")
	replaceGuestShell(argv)
	argv, hooks, sessionUUID := processAgentArgv(argv, a, agent, known, inline, hookBin)

	name := microVMSessionName(a.Name, a.Cwd, kind)
	agentsDigest := resolveMicroVMAgentsDigest()
	systemMode := "reset"
	if a.KeepSystem {
		systemMode = "keep"
	}

	extraPathDirs := []string{"/opt/agents/bin"}
	env, term := processWalledEnv(a, id, extraPathDirs, agent, inline)
	env = applyUserAndIdentityEnvWithBrowser(env, a.Env, id, a.HostName, kind, name, process.GuestRunDir()+"/browser")

	now := time.Now().UTC()
	m := &Manifest{
		Schema: 1, ID: id, Name: name, CreatedAt: now, HostVersion: version.Version,
		Agent:   AgentInfo{Kind: kind, Argv: argv},
		Cwd:     "/work",
		Env:     env,
		Term:    TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:  resolveSessionLimits(a.MemoryMax, a.CPUs, a.DiskMax, kind, true),
		Run:     Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:  Status{State: StateStarting, Since: now},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Image: &ManifestImage{
			Ref:            imgRec.Ref,
			Digest:         imgRec.Digest,
			PlatformDigest: imgRec.PlatformDigest,
			RootHash:       imgRec.RootHash,
		},
		Agents: agentsDigest,
		Wall: &WallRecord{
			Level:            api.LevelMicroVM,
			Driver:           chv.DriverName,
			Version:          chvVer,
			Preset:           string(egress.PresetBalanced),
			WorkspaceGuest:   "/work",
			StateDirs:        slices.Clone(agent.StateDirs),
			CacheDirs:        cacheDirs,
			At:               now,
			PolicyTOML:       buildSessionPolicyTOML(string(egress.PresetBalanced), ws),
			PackageManagers:  ws.pkgLevel,
			MinReleaseAge:    ws.minAge,
			ModelBudget:      ws.modelBudget,
			UploadBudget:     ws.uploadBudget,
			SandboxEnv:       filterSandboxEnv(a.Env, a.SandboxEnv),
			BoundSecrets:     boundSecrets,
			ExposedSecrets:   exposedSecrets,
			SubscriptionAuth: subscriptionAuth,
			IdleStop:         ws.idleStop,
			Runtime:          bundle.Label,
			RuntimeFiles:     slices.Clone(bundle.Files),
			System:           systemMode,
			Sudo:             a.Sudo,
			Docker:           a.Docker,
			ImageRef:         a.Image,
		},
		HostName:    a.HostName,
		SandboxName: name,
		CopyIn:      a.CopyIn,
	}
	if ws.modelBudget > 0 {
		m.Usage = &api.SessionUsage{Budget: ws.modelBudget}
	}
	m.Secrets = secrets
	fillAgentManifest(m, a.Argv, argv, agent, known, inline, hooks, sessionUUID)
	if a.DeferStart {
		return saveDeferredSession(m)
	}
	return startSession(m, a.Secrets)
}

func resolveMicroVMCaches(keep *bool, extra []string) ([]string, error) {
	keepCaches := true
	if keep != nil {
		keepCaches = *keep
	}
	cacheDirs, err := sandbox.ResolveCaches(keepCaches, extra)
	if err != nil {
		msg, hint := err.Error(), "Check sandbox.caches in config.toml or hosts.toml."
		if before, after, hasTry := strings.Cut(msg, ". Try: "); hasTry {
			msg, hint = before, after
		}
		return nil, UserError("cache_path_invalid", msg, hint)
	}
	return cacheDirs, nil
}

// replaceGuestShell substitutes /bin/sh for "$SHELL" inside microvms so the
// host's login shell (for example /usr/bin/zsh) is not requested inside the
// guest rootfs.
func replaceGuestShell(argv []string) {
	for i := range argv {
		if argv[i] == "$SHELL" {
			argv[i] = "/bin/sh"
		}
	}
}

func microVMSessionName(rawName, rawCwd, kind string) string {
	name := strings.TrimSpace(rawName)
	if name != "" {
		return name
	}
	if raw := strings.TrimSpace(rawCwd); raw != "" && raw != "~" && raw != "/" {
		name = filepath.Base(expandHome(raw))
	}
	if name == "" || name == "." || name == "/" {
		return kind
	}
	return name
}

// resolveMicroVMAgentsDigest builds or reuses the host's read-only agents disk
// when agents are installed on the host, falling back to any already-stored
// agents disk. Tests replace it to stay hermetic against host PATH.
var resolveMicroVMAgentsDigest = defaultResolveMicroVMAgentsDigest

func defaultResolveMicroVMAgentsDigest() string {
	actx, acancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer acancel()
	if sources := InstalledAgentSources(actx); len(sources) > 0 {
		if rec, _, err := ImageStore().EnsureAgentsDisk(actx, sources, hostAgentsDiskOptions()); err == nil && rec.Digest != "" {
			return rec.Digest
		}
	}
	if rec, err := ImageStore().LatestAgentsDisk(); err == nil && rec.Digest != "" {
		return rec.Digest
	}
	return ""
}

func resolveMicroVMImage(query string) (api.ImageRecord, error) {
	store := ImageStore()
	q := strings.TrimSpace(query)
	if q != "" && q != "default" {
		rec, _, err := store.Get(q)
		if err != nil {
			return api.ImageRecord{}, imageProtoError(err)
		}
		return rec, nil
	}
	if dp, err := image.DefaultForArch(runtime.GOARCH); err == nil {
		if rec, _, gerr := store.Get(dp.PlatformDigest); gerr == nil {
			return rec, nil
		}
	}
	list, err := store.List()
	if err != nil {
		return api.ImageRecord{}, imageProtoError(err)
	}
	for _, rec := range list {
		if rec.Arch == "" || rec.Arch == runtime.GOARCH {
			return rec, nil
		}
	}
	return api.ImageRecord{}, UserError(
		"image_missing",
		"no microvm root image is stored on this host",
		brand.Name+" image pull default <host>",
	)
}
