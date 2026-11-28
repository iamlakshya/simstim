package host

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

func checkNewSessionArgs(a NewArgs) error {
	if err := checkMemoryMax(a.MemoryMax); err != nil {
		return err
	}
	if err := checkCPUs(a.CPUs); err != nil {
		return err
	}
	if err := checkDiskMax(a.DiskMax); err != nil {
		return err
	}
	return checkUserEnv(a.Env)
}

func checkCPUs(cpus int) error {
	if cpus < api.CPUsAuto {
		return UserError("bad_cpus", fmt.Sprintf("invalid CPU cap %d", cpus), "Pass a positive whole number of CPUs, such as --cpus 2.")
	}
	return nil
}

func checkDiskMax(diskMax int64) error {
	if diskMax < 0 {
		return UserError("bad_disk_max", fmt.Sprintf("invalid disk cap %d", diskMax), "Pass a positive size such as 10G.")
	}
	return nil
}

func checkUserEnv(env map[string]string) error {
	for k, v := range env {
		if !validEnvKey(k) {
			return UserError("bad_env", fmt.Sprintf("invalid environment variable name %q", api.CleanText(k, 64)),
				"Use letters, digits and underscores, starting with a letter or underscore.")
		}
		if strings.ContainsRune(v, 0) {
			return UserError("bad_env", fmt.Sprintf("environment variable %q contains a NUL byte", k),
				"Remove NUL bytes from the variable value.")
		}
	}
	return nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i := range len(k) {
		c := k[i]
		letter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		digit := c >= '0' && c <= '9'
		if i == 0 && !letter {
			return false
		}
		if !letter && !digit {
			return false
		}
	}
	return true
}

func resolveSessionLimits(askedMem int64, askedCPUs int, diskMax int64, kind string, walled bool) Limits {
	if askedMem < 0 {
		askedMem = 0
	}
	if askedCPUs < 0 {
		askedCPUs = 0
	}
	mem := sessionMemoryMax(askedMem)
	cpus := askedCPUs
	if walled {
		defCPUs, defMem := agents.DefaultSize(kind)
		if askedMem == 0 {
			mem = sessionMemoryMax(defMem)
		}
		if askedCPUs == 0 {
			cpus = defCPUs
		}
	}
	return Limits{
		MemoryMax:   mem,
		MemoryAsked: askedMem,
		CPUs:        cpus,
		CPUsAsked:   askedCPUs,
		DiskMax:     max(diskMax, 0),
	}
}

// applyUserAndIdentityEnv merges user-configured env vars into base and always
// forces the session's identity variables (and BROWSER on walled sessions) on
// top so identity vars win over all layers (spec 17.2).
func applyUserAndIdentityEnv(base []string, userEnv map[string]string, id, hostName, agentKind, sandboxName string, walled bool) []string {
	browserPath := ""
	if walled {
		browserPath = filepath.Join(paths.GuestRuntimeDir(id), "browser")
	}
	return applyUserAndIdentityEnvWithBrowser(base, userEnv, id, hostName, agentKind, sandboxName, browserPath)
}

func applyUserAndIdentityEnvWithBrowser(base []string, userEnv map[string]string, id, hostName, agentKind, sandboxName, browserPath string) []string {
	if hostName == "" {
		hostName, _ = os.Hostname()
	}
	overrides := make(map[string]string, len(userEnv)+6)
	for k, v := range userEnv {
		if strings.HasPrefix(k, brand.EnvPrefix) {
			continue
		}
		overrides[k] = v
	}
	if browserPath != "" {
		overrides["BROWSER"] = browserPath
	}
	overrides[brand.Env("SESSION")] = id
	if hostName != "" {
		overrides[brand.Env("HOST")] = hostName
	}
	if agentKind != "" {
		overrides[brand.Env("AGENT")] = agentKind
	}
	if sandboxName != "" {
		overrides[brand.Env("SANDBOX")] = sandboxName
	}

	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[k]; replaced {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

// filterSandboxEnv merges sandbox.env defaults and session env overrides for
// WallRecord.SandboxEnv, stripping protected identity variables
// (brand.EnvPrefix) and BROWSER so guest init's ApplyPackageSettings cannot
// overwrite them (spec 17.2).
func filterSandboxEnv(sessionEnv, sandboxEnv map[string]string) map[string]string {
	if len(sessionEnv) == 0 && len(sandboxEnv) == 0 {
		return nil
	}
	out := make(map[string]string, len(sessionEnv)+len(sandboxEnv))
	for k, v := range sandboxEnv {
		if isProtectedSandboxEnv(k) {
			continue
		}
		out[k] = v
	}
	for k, v := range sessionEnv {
		if isProtectedSandboxEnv(k) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func isProtectedSandboxEnv(k string) bool {
	return k == "BROWSER" || strings.HasPrefix(k, brand.EnvPrefix)
}

func resolveNewCwd(id string, a NewArgs) (cwd string, missing []string, err error) {
	if a.SeedWorkspace && a.Cwd == "" {
		return paths.WorkDir(id), nil, nil
	}
	cwd = expandHome(a.Cwd)
	missing, err = checkFolder(cwd, a.Worktree)
	return cwd, missing, err
}

func ensureNewCwd(cwd string, missing []string, a NewArgs) (cleanup func(), err error) {
	if a.SeedWorkspace && a.Cwd == "" {
		return nil, paths.EnsurePrivateDir(cwd)
	}
	if len(missing) == 0 {
		return nil, nil
	}
	if !a.MakeFolder {
		return nil, folderQuestion(cwd)
	}
	if err := makeFolders(cwd); err != nil {
		return nil, err
	}
	return func() { removeFolders(missing) }, nil
}

func saveDeferredSession(m *Manifest) (*SessionInfo, error) {
	if err := paths.EnsurePrivateDir(paths.SessionDir(m.ID)); err != nil {
		return nil, err
	}
	if err := paths.EnsurePrivateDir(paths.StageDir(m.ID)); err != nil {
		return nil, err
	}
	m.Status = Status{State: StateStopped, Since: m.CreatedAt, Reason: "waiting for laptop files"}
	if err := m.Save(); err != nil {
		return nil, err
	}
	info := infoFrom(m, false)
	return &info, nil
}
