package host

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// allAgentStateDirs returns the deduplicated union of StateDirs across every
// agent in the agent table so any agent can join a running sandbox without a
// restart (isolation spec 6.11).
func allAgentStateDirs() []string {
	var out []string
	for _, a := range agents.Table {
		for _, rel := range a.StateDirs {
			if rel == "" || slices.Contains(out, rel) {
				continue
			}
			out = append(out, rel)
		}
	}
	return out
}

// validateKeepFiles validates and deduplicates [sandbox] keep paths, returning
// a keep_refused UserError if any entry is disallowed (spec 6.11).
func validateKeepFiles(keep []string) ([]string, error) {
	if len(keep) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(keep))
	for _, raw := range keep {
		clean, err := api.CheckKeepPath(raw)
		if err != nil {
			return nil, UserError(api.KeepRefusedCode, err.Error(), "Remove shell startup and hook files (such as .bashrc or .profile) from [sandbox] keep.")
		}
		if !slices.Contains(out, clean) {
			out = append(out, clean)
		}
	}
	return out, nil
}

type agentMountBuilder struct {
	home     string
	mounted  map[string]string
	aliases  map[string]bool
	mounts   []WallMount
	pathDirs []string
	idx      int
}

func (b *agentMountBuilder) addMount(src, dest string) string {
	src = filepath.Clean(src)
	if existing, ok := b.mounted[src]; ok {
		return existing
	}
	b.mounted[src] = dest
	b.mounts = append(b.mounts, WallMount{Source: src, Dest: dest, ReadOnly: true})
	return dest
}

func (b *agentMountBuilder) addAlias(realBin, name string) string {
	const aliasDir = "/opt/agents/bin"
	aliasPath := aliasDir + "/" + name
	if !b.aliases[name] {
		b.aliases[name] = true
		b.mounts = append(b.mounts, WallMount{Source: filepath.Clean(realBin), Dest: aliasPath, ReadOnly: true})
		b.pathDirs = appendUnique(b.pathDirs, aliasDir)
	}
	return aliasPath
}

func (b *agentMountBuilder) mountBinary(realBin string) string {
	if mapped, ok := mapInsideExisting(realBin, b.mounted); ok {
		b.pathDirs = appendUnique(b.pathDirs, filepath.Dir(mapped))
		return mapped
	}
	dest, binDir := mountNonSystemBinary(realBin, b.home, &b.idx, b.addMount)
	b.pathDirs = appendUnique(b.pathDirs, binDir)
	return dest
}

func (b *agentMountBuilder) mountOne(resolvedBin, searchPath string, ag agents.Agent) string {
	if b.home != "" {
		mountInstallDirs(b.home, ag.InstallDirs, &b.idx, b.addMount)
	}
	if resolvedBin == "" {
		return ""
	}
	realBin, err := filepath.EvalSymlinks(resolvedBin)
	if err != nil {
		realBin = resolvedBin
	}
	guestPath := realBin
	if !isSystemPath(realBin) {
		guestPath = b.mountBinary(realBin)
		origBase := filepath.Base(resolvedBin)
		if origBase != "" && origBase != "." && origBase != filepath.Base(guestPath) {
			guestPath = b.addAlias(realBin, origBase)
		}
	}
	b.mountInterpreter(realBin, searchPath)
	return guestPath
}

func (b *agentMountBuilder) mountInterpreter(realBin, searchPath string) {
	interp := shebangInterpreter(realBin, searchPath)
	if interp == "" || isSystemPath(interp) {
		return
	}
	if realInterp, err := filepath.EvalSymlinks(interp); err == nil {
		interp = realInterp
	}
	if !isSystemPath(interp) {
		_ = b.mountBinary(interp)
	}
}

// mapAllAgentMounts computes read-only mounts under /opt/agents/ for the
// primary session's agent AND every other installed agent in agents.Table so
// `new --in <s> --agent <other>` works without restarting the sandbox (spec 6.11).
func mapAllAgentMounts(primaryResolved, primarySearch string, primary agents.Agent) (guestArg0 string, mounts []WallMount, pathDirs []string) {
	home, _ := os.UserHomeDir()
	if real, rerr := filepath.EvalSymlinks(home); home != "" && rerr == nil {
		home = real
	}
	b := &agentMountBuilder{
		home:    filepath.Clean(home),
		mounted: map[string]string{},
		aliases: map[string]bool{},
	}
	if home == "" {
		b.home = ""
	}
	guestArg0 = b.mountOne(primaryResolved, primarySearch, primary)
	for _, other := range agents.Table {
		if other.Kind == "shell" || other.Kind == primary.Kind {
			continue
		}
		otherSearch := processSearchPath(other)
		b.mountOne(findInstalledAgentBin(other, otherSearch), otherSearch, other)
	}
	return guestArg0, b.mounts, b.pathDirs
}

func findInstalledAgentBin(ag agents.Agent, searchPath string) string {
	for _, binName := range ag.Binaries {
		if p, err := resolveAgentBinary(binName, searchPath, ag); err == nil {
			return p
		}
	}
	return ""
}

// resolveSandboxShell chooses the shell executable inside a sandbox:
// preferred (from [sandbox] shell), else the host's login shell when it is a
// system binary, else /bin/bash if present, else /bin/sh.
func resolveSandboxShell(preferred string) string {
	if s := strings.TrimSpace(preferred); s != "" {
		return s
	}
	if sh := LoginShell(); isSystemPath(sh) {
		if fi, err := os.Stat(sh); err == nil && !fi.IsDir() {
			return sh
		}
	}
	if fi, err := os.Stat("/bin/bash"); err == nil && !fi.IsDir() {
		return "/bin/bash"
	}
	return "/bin/sh"
}

// mapJoinedAgentArgv resolves the command for a new session joining an
// existing sandbox (primary manifest m), mapping any host agent binary through
// m.Wall.ExtraRO into the guest path.
func mapJoinedAgentArgv(primary *Manifest, a NewArgs) (argv []string, kind string, agent agents.Agent, known bool, hooks []string, sessionUUID string, inline bool, err error) {
	argv, kind, agent, known, err = pickAgent(a)
	if err != nil {
		return nil, "", agents.Agent{}, false, nil, "", false, err
	}
	inline = a.Inline && known && len(a.Argv) == 0 && agent.Inline.Has()
	hookBin := guestHookHelperPath()

	if len(a.Argv) == 0 && kind == "shell" {
		pref := strings.TrimSpace(a.Shell)
		if pref == "" && primary.Wall != nil {
			pref = primary.Wall.Shell
		}
		argv = []string{resolveSandboxShell(pref)}
		return argv, kind, agent, known, nil, "", false, nil
	}

	argv, hooks, sessionUUID = processAgentArgv(argv, a, agent, known, inline, hookBin)

	// Custom commands (-- <cmd>) run inside the guest using the guest's PATH
	// unless argv[0] matches a host binary already mounted into the sandbox.
	if len(a.Argv) > 0 {
		return argv, kind, agent, known, hooks, sessionUUID, inline, nil
	}

	// Known agent joining an existing sandbox: map its binary onto the guest's
	// already-mounted /opt/agents/ path or system path.
	searchPath := processSearchPath(agent)
	resolved, rerr := resolveAgentBinary(argv[0], searchPath, agent)
	if rerr != nil {
		return nil, "", agents.Agent{}, false, nil, "", false, UserError("agent_missing", argv[0]+" is not installed on this host", "Install it on the host, then try again.")
	}
	realBin, serr := filepath.EvalSymlinks(resolved)
	if serr != nil {
		realBin = resolved
	}
	if isSystemPath(realBin) {
		argv[0] = realBin
		return argv, kind, agent, known, hooks, sessionUUID, inline, nil
	}
	if primary.Wall != nil {
		mounted := make(map[string]string, len(primary.Wall.ExtraRO))
		for _, wm := range primary.Wall.ExtraRO {
			mounted[filepath.Clean(wm.Source)] = wm.Dest
		}
		// Prefer /opt/agents/bin/<origBase> alias when present.
		origBase := filepath.Base(resolved)
		for _, wm := range primary.Wall.ExtraRO {
			if wm.Dest == "/opt/agents/bin/"+origBase {
				argv[0] = wm.Dest
				return argv, kind, agent, known, hooks, sessionUUID, inline, nil
			}
		}
		if mapped, ok := mapInsideExisting(realBin, mounted); ok {
			argv[0] = mapped
			return argv, kind, agent, known, hooks, sessionUUID, inline, nil
		}
	}
	// Fallback: rely on guest PATH lookup by command name.
	argv[0] = filepath.Base(resolved)
	return argv, kind, agent, known, hooks, sessionUUID, inline, nil
}
