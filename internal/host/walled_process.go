package host

import (
	"bufio"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/version"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// probeProcessWall checks whether this host can run bubblewrap process walls.
// Tests replace it.
var probeProcessWall = func(ctx context.Context) (ok bool, ver, reason, fix string) {
	p := process.Driver{}.Probe(ctx)
	return p.OK, p.Version, p.Reason, p.Fix
}

func newProcessWalled(a NewArgs) (_ *SessionInfo, err error) {
	pctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ok, bwrapVer, reason, fix := probeProcessWall(pctx)
	cancel()
	if !ok {
		if fix == "" {
			fix = "Install bubblewrap on the host and check host doctor."
		}
		return nil, UserError("wall_setup", reason, fix)
	}
	return newSandboxedWalled(a, "process", process.DriverName, bwrapVer)
}

func newSandboxedWalled(a NewArgs, level, driver, ver string) (_ *SessionInfo, err error) {
	if err := checkMemoryMax(a.MemoryMax); err != nil {
		return nil, err
	}
	pkgLevel, err := api.ParsePackageManagers(a.PackageManagers)
	if err != nil {
		return nil, UserError("package_level_invalid", api.PackageLevelMessage(a.PackageManagers), api.PackageLevelHint)
	}
	_, minAge, err := api.ParseMinReleaseAge(a.MinReleaseAge)
	if err != nil {
		return nil, UserError("release_age_invalid", api.ReleaseAgeMessage(a.MinReleaseAge), api.ReleaseAgeHint)
	}
	argv, kind, agent, known, err := pickAgent(a)
	if err != nil {
		return nil, err
	}
	secrets, err := checkSecrets(a.Secrets)
	if err != nil {
		return nil, err
	}

	id := NewID()
	inline := a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	hookBin := guestHookHelperPath()
	argv, hooks, sessionUUID := processAgentArgv(argv, a, agent, known, inline, hookBin)

	searchPath := processSearchPath(agent)
	resolved, err := resolveAgentBinary(argv[0], searchPath, agent)
	if err != nil {
		return nil, UserError("agent_missing", argv[0]+" is not installed on this host", "Install it on the host, then try again.")
	}

	wsHost, wsGuest, gi, missing, err := prepareProcessWorkspace(a, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && len(missing) > 0 {
			removeFolders(missing)
		}
	}()

	guestArg0, extraRO, extraPathDirs := mapAgentMounts(resolved, searchPath, agent)
	argv[0] = guestArg0
	env, term := processWalledEnv(a, id, extraPathDirs, agent, inline)

	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = filepath.Base(wsGuest)
	}
	asked := max(a.MemoryMax, 0)
	now := time.Now().UTC()
	m := &Manifest{
		Schema: 1, ID: id, Name: name, CreatedAt: now, HostVersion: version.Version,
		Agent:   AgentInfo{Kind: kind, Argv: argv},
		Cwd:     wsGuest,
		Env:     env,
		Term:    TermInfo{Term: term, Cols: a.Cols, Rows: a.Rows},
		Limits:  Limits{MemoryMax: sessionMemoryMax(asked), MemoryAsked: asked},
		Run:     Runtime{Socket: paths.Socket(id), BootID: BootID()},
		Status:  Status{State: StateStarting, Since: now},
		Restart: Restart{Policy: "resume", MaxAttempts: 3},
		Wall: &WallRecord{
			Level:           level,
			Driver:          driver,
			Version:         ver,
			Preset:          string(egress.PresetBalanced),
			WorkspaceHost:   wsHost,
			WorkspaceGuest:  wsGuest,
			StateDirs:       slices.Clone(agent.StateDirs),
			ExtraRO:         extraRO,
			At:              now,
			PackageManagers: pkgLevel,
			MinReleaseAge:   minAge,
			SandboxEnv:      maps.Clone(a.SandboxEnv),
		},
	}
	m.Git = gi
	m.Secrets = secrets
	fillAgentManifest(m, a.Argv, argv, agent, known, inline, hooks, sessionUUID)
	return startSession(m, a.Secrets)
}

func guestHookHelperPath() string {
	base := brand.HostBinary
	if exe, err := holderExecutable(); err == nil && filepath.Base(exe) != "" {
		base = filepath.Base(exe)
	}
	return process.GuestHelperDir() + "/" + base
}

func processAgentArgv(argv []string, a NewArgs, agent agents.Agent, known, inline bool, hookBin string) (out, hooks []string, sessionUUID string) {
	vars := map[string]string{"session_uuid": newUUID()}
	argv = agents.Expand(argv, vars)
	for i, s := range argv {
		if s == "$SHELL" {
			sh := LoginShell()
			if !isSystemPath(sh) {
				sh = "/bin/sh"
			}
			argv[i] = sh
		}
	}
	if strings.HasPrefix(argv[0], "~/") {
		argv[0] = expandHome(argv[0])
	}
	if known && len(agent.Hooks) > 0 && len(a.Argv) == 0 {
		hooks = hookArgsWithBin(agent.Hooks, hookBin)
		argv = append(argv, hooks...)
	}
	if inline {
		argv = append(argv, agent.Inline.Args...)
	}
	return argv, hooks, vars["session_uuid"]
}

// processSearchPath returns PATH from the login shell plus common user agent
// bin folders (such as ~/.local/bin) used only on the trusted side to find the
// agent binary; never passed into the sandbox.
func processSearchPath(agent agents.Agent) string {
	base := walledSearchPath()
	home, _ := os.UserHomeDir()
	var extras []string
	if home != "" {
		extras = append(extras, filepath.Join(home, ".local", "bin"))
		if agent.Setup != nil {
			for _, p := range agent.Setup.Paths {
				dir := filepath.Dir(expandHome(p))
				if dir != "" && dir != "." && !slices.Contains(extras, dir) {
					extras = append(extras, dir)
				}
			}
		}
	}
	if len(extras) == 0 {
		return base
	}
	if base == "" {
		return strings.Join(extras, string(os.PathListSeparator))
	}
	return base + string(os.PathListSeparator) + strings.Join(extras, string(os.PathListSeparator))
}

func resolveAgentBinary(name, searchPath string, agent agents.Agent) (string, error) {
	if p, err := lookPath(name, searchPath); err == nil {
		return p, nil
	}
	if !strings.Contains(name, "/") && agent.Setup != nil {
		for _, p := range agent.Setup.Paths {
			cand := expandHome(p)
			if filepath.Base(cand) == name {
				if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
					return cand, nil
				}
			}
		}
	}
	return "", os.ErrNotExist
}

// prepareProcessWorkspace resolves the host workspace directory and its guest
// /work/... mount target. A default "~" (host home) or "/" is redirected to a
// private per-session scratch folder so a process wall never mounts host $HOME.
func prepareProcessWorkspace(a NewArgs, id string) (wsHost, wsGuest string, gi *GitInfo, missing []string, err error) {
	hostHome, _ := os.UserHomeDir()
	raw := strings.TrimSpace(a.Cwd)
	cwd := expandHome(raw)
	cleanCwd := filepath.Clean(cwd)
	isDefaultOrHome := raw == "" || raw == "~" || cleanCwd == "/" || (hostHome != "" && cleanCwd == filepath.Clean(hostHome))
	if isDefaultOrHome && !a.Worktree {
		wsHost = paths.WorkDir(id)
		if err := paths.EnsurePrivateDir(wsHost); err != nil {
			return "", "", nil, nil, err
		}
		return wsHost, "/work/scratch", nil, nil, nil
	}
	missing, err = checkFolder(cwd, a.Worktree)
	if err != nil {
		return "", "", nil, nil, err
	}
	if len(missing) > 0 {
		if !a.MakeFolder {
			return "", "", nil, nil, folderQuestion(cwd)
		}
		if err := makeFolders(cwd); err != nil {
			return "", "", nil, nil, err
		}
	}
	if a.Worktree {
		n := a.Name
		if strings.TrimSpace(n) == "" {
			n = filepath.Base(cwd)
		}
		gi, err = makeWorktree(cwd, n, id, a.Base)
		if err != nil {
			return "", "", nil, missing, err
		}
		cwd = gi.Worktree
	}
	return cwd, process.WorkspaceGuestPath(cwd), gi, missing, nil
}

// mapAgentMounts computes read-only mounts under /opt/agents/ for an agent
// binary (and any shebang interpreter or agent InstallDirs) that live outside
// the read-only system trees (/usr, /bin, /sbin, /lib, /lib64), never exposing
// the host user's home folder path.
func mapAgentMounts(resolvedBin, searchPath string, agent agents.Agent) (guestArg0 string, mounts []WallMount, pathDirs []string) {
	realBin, err := filepath.EvalSymlinks(resolvedBin)
	if err != nil {
		realBin = resolvedBin
	}
	// Resolve the home folder as the binary is resolved, so a home behind a
	// symlink (such as /home -> /var/home) still matches the binary's path.
	home, _ := os.UserHomeDir()
	if real, rerr := filepath.EvalSymlinks(home); home != "" && rerr == nil {
		home = real
	}
	cleanHome := filepath.Clean(home)
	mounted := map[string]string{} // hostSrc -> guestDest
	idx := 0
	addMount := func(src, dest string) string {
		src = filepath.Clean(src)
		if existing, ok := mounted[src]; ok {
			return existing
		}
		mounted[src] = dest
		mounts = append(mounts, WallMount{Source: src, Dest: dest, ReadOnly: true})
		return dest
	}

	if home != "" {
		mountInstallDirs(cleanHome, agent.InstallDirs, &idx, addMount)
	}

	guestArg0 = realBin
	if !isSystemPath(realBin) {
		if mapped, ok := mapInsideExisting(realBin, mounted); ok {
			guestArg0 = mapped
			pathDirs = appendUnique(pathDirs, filepath.Dir(mapped))
		} else {
			dest, binDir := mountNonSystemBinary(realBin, cleanHome, &idx, addMount)
			guestArg0 = dest
			pathDirs = appendUnique(pathDirs, binDir)
		}
		// If the command was found via a symlink with a different basename
		// (for example ~/.local/bin/claude -> ~/.local/share/claude/versions/2.1.332),
		// also bind the resolved binary under its command name on guest PATH so
		// resume commands ("claude --resume ...") and subshells find it by name.
		origBase := filepath.Base(resolvedBin)
		if origBase != "" && origBase != "." && origBase != filepath.Base(guestArg0) {
			aliasDir := "/opt/agents/bin"
			aliasPath := aliasDir + "/" + origBase
			mounts = append(mounts, WallMount{Source: filepath.Clean(realBin), Dest: aliasPath, ReadOnly: true})
			guestArg0 = aliasPath
			pathDirs = appendUnique(pathDirs, aliasDir)
		}
	}

	// If the binary is a script starting with #!/usr/bin/env <name> or #!<path>,
	// ensure a non-system interpreter (for example nvm node) is also mounted RO.
	if interp := shebangInterpreter(realBin, searchPath); interp != "" && !isSystemPath(interp) {
		if realInterp, rerr := filepath.EvalSymlinks(interp); rerr == nil {
			interp = realInterp
		}
		if !isSystemPath(interp) {
			if mapped, ok := mapInsideExisting(interp, mounted); ok {
				pathDirs = appendUnique(pathDirs, filepath.Dir(mapped))
			} else {
				_, interpDir := mountNonSystemBinary(interp, cleanHome, &idx, addMount)
				pathDirs = appendUnique(pathDirs, interpDir)
			}
		}
	}
	return guestArg0, mounts, pathDirs
}

// mountInstallDirs mounts the agent's install folders that exist under home
// read-only under /opt/agents/.
func mountInstallDirs(home string, rels []string, idx *int, addMount func(src, dest string) string) {
	for _, rel := range rels {
		hostDir := filepath.Clean(filepath.Join(home, rel))
		if real, err := filepath.EvalSymlinks(hostDir); err == nil {
			hostDir = real
		}
		// An install folder that is, or links to, the home folder would put
		// all of home in the sandbox.
		if hostDir == home {
			continue
		}
		if fi, err := os.Stat(hostDir); err == nil && fi.IsDir() {
			addMount(hostDir, fmt.Sprintf("/opt/agents/install-%d", *idx))
			*idx++
		}
	}
}

func mapInsideExisting(path string, mounted map[string]string) (string, bool) {
	clean := filepath.Clean(path)
	for hostSrc, guestDest := range mounted {
		if rel, err := filepath.Rel(hostSrc, clean); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return guestDest, true
			}
			return guestDest + "/" + filepath.ToSlash(rel), true
		}
	}
	return "", false
}

func mountNonSystemBinary(realBin, cleanHome string, idx *int, addMount func(src, dest string) string) (guestBin, guestDir string) {
	dir := filepath.Dir(realBin)
	parent := filepath.Dir(dir)
	// When the binary sits in a dedicated toolchain's bin/ folder (such as
	// ~/.nvm/versions/node/v22/bin/node), mount the toolchain root so sibling
	// lib/ files work, while never mounting $HOME, ~/.local, /, /tmp or /var.
	if filepath.Base(dir) == "bin" && safeToolchainRoot(parent, cleanHome) {
		rootDest := fmt.Sprintf("/opt/agents/tool-%d", *idx)
		*idx++
		addMount(parent, rootDest)
		guestDir = rootDest + "/bin"
		return guestDir + "/" + filepath.Base(realBin), guestDir
	}
	// Otherwise mount only the single executable file so no sibling files from
	// ~/.local/bin or /tmp enter the sandbox.
	guestDir = fmt.Sprintf("/opt/agents/bin-%d", *idx)
	*idx++
	guestBin = guestDir + "/" + filepath.Base(realBin)
	addMount(realBin, guestBin)
	return guestBin, guestDir
}

func safeToolchainRoot(dir, cleanHome string) bool {
	c := filepath.Clean(dir)
	switch c {
	case "/", "/tmp", "/var", "/usr", "/opt", "/home", cleanHome:
		return false
	}
	if cleanHome != "" && (c == filepath.Join(cleanHome, ".local") || c == filepath.Join(cleanHome, ".config")) {
		return false
	}
	return true
}

func isSystemPath(p string) bool {
	c := filepath.Clean(p)
	for _, prefix := range []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/"} {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func shebangInterpreter(path, searchPath string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "#!")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if filepath.Base(fields[0]) == "env" && len(fields) >= 2 {
		name := fields[1]
		if name == "-S" && len(fields) >= 3 {
			name = fields[2]
		}
		if p, lerr := lookPath(name, searchPath); lerr == nil {
			return p
		}
		return ""
	}
	return fields[0]
}

func processWalledEnv(a NewArgs, id string, extraPathDirs []string, agent agents.Agent, inline bool) (env []string, term string) {
	term = a.Term
	if !HasTerminfo(term) {
		term = "xterm-256color"
	}
	colorterm := a.ColorTerm
	if colorterm == "" {
		colorterm = "truecolor"
	}
	pathDirs := append([]string(nil), extraPathDirs...)
	for _, d := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
		pathDirs = appendUnique(pathDirs, d)
	}
	vars := map[string]string{
		"HOME":                   process.GuestHome,
		"USER":                   "agent",
		"LOGNAME":                "agent",
		"LANG":                   "C.UTF-8",
		"PATH":                   strings.Join(pathDirs, string(os.PathListSeparator)),
		"TERM":                   term,
		"COLORTERM":              colorterm,
		brand.Env("SESSION"):     id,
		"HTTP_PROXY":             process.GuestProxyURL,
		"HTTPS_PROXY":            process.GuestProxyURL,
		"ALL_PROXY":              process.GuestProxyURL,
		"http_proxy":             process.GuestProxyURL,
		"https_proxy":            process.GuestProxyURL,
		"all_proxy":              process.GuestProxyURL,
		"NO_PROXY":               "localhost,127.0.0.1,::1",
		"no_proxy":               "localhost,127.0.0.1,::1",
		"NODE_USE_ENV_PROXY":     "1",
		"npm_config_proxy":       process.GuestProxyURL,
		"npm_config_https_proxy": process.GuestProxyURL,
		"CARGO_HTTP_PROXY":       process.GuestProxyURL,
	}
	maps.Copy(vars, agent.ProxyEnv)
	if a.TermProgram != "" {
		vars["TERM_PROGRAM"] = a.TermProgram
	}
	if a.TermProgramVersion != "" {
		vars["TERM_PROGRAM_VERSION"] = a.TermProgramVersion
	}
	if inline {
		for k, v := range agent.Inline.Env {
			if v != "" {
				vars[k] = v
			}
		}
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env = make([]string, 0, len(keys))
	for _, k := range keys {
		env = append(env, k+"="+vars[k])
	}
	return env, term
}

func appendUnique(list []string, v string) []string {
	if v == "" || v == "." || slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}
