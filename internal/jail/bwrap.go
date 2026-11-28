package jail

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var baseEtcPaths = []string{
	"/etc/ld.so.cache",
	"/etc/ld.so.conf",
	"/etc/ld.so.conf.d",
	"/etc/localtime",
}

var netHostEtcPaths = []string{
	"/etc/resolv.conf",
	"/etc/hosts",
	"/etc/nsswitch.conf",
	"/etc/services",
	"/etc/protocols",
	"/etc/ssl",
	"/etc/ca-certificates",
	"/etc/ca-certificates.conf",
	"/etc/pki",
}

// BuildBwrapArgs validates s and builds the bubblewrap argument list for it
// (without a seccomp fd or --disable-userns). Callers launching on Linux should
// use Command so seccomp and the no-userns Landlock fallback are applied.
func BuildBwrapArgs(s Spec) ([]string, error) {
	return buildBwrapArgsWithFS(s, -1, false, defaultJailFS)
}

func buildBwrapArgsWithFS(s Spec, seccompFD int, disableUserNS bool, fsOps jailFS) ([]string, error) {
	if err := validateSpec(s, fsOps); err != nil {
		return nil, err
	}
	args := make([]string, 0, 48+len(s.Binds)*3+len(s.Env)*3+len(s.Argv))
	args = append(args,
		"--unshare-user",
		"--unshare-ipc",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-cgroup-try",
	)
	if s.Net == NetNone {
		args = append(args, "--unshare-net")
	}
	// Do not pass --uid/--gid or call setgroups: unprivileged bwrap keeps the
	// caller's supplementary groups (such as kvm for a vmm jail, spec 14.4.2).
	args = append(args,
		"--cap-drop", "ALL",
		"--die-with-parent",
		"--new-session",
	)
	if disableUserNS {
		args = append(args, "--disable-userns")
	}

	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	)
	args = appendSystemMounts(args, fsOps)
	args = appendMinimalEtcMounts(args, s.Net, fsOps)

	seenDest := map[string]bool{}
	for _, b := range s.Binds {
		src := filepath.Clean(b.Src)
		dest := src
		if b.Dest != "" {
			dest = filepath.Clean(b.Dest)
		}
		if seenDest[dest] {
			continue
		}
		seenDest[dest] = true
		switch b.Mode {
		case BindRO:
			args = append(args, "--ro-bind", src, dest)
		case BindRW:
			args = append(args, "--bind", src, dest)
		case BindDev:
			args = append(args, "--dev-bind", src, dest)
		}
	}

	// Ensure Argv[0] is visible read-only when it lives outside /usr, /bin,
	// /sbin, /lib or /lib64 (for example ~/.local/share/.../bin/ or a test binary).
	exePath := resolveTargetPath(s.Argv[0], fsOps)
	args = appendExeBind(args, exePath, seenDest, fsOps)

	if s.Dir != "" {
		args = append(args, "--chdir", filepath.Clean(s.Dir))
	}

	args = append(args, "--clearenv")
	for _, kv := range buildEnvList(s.Env) {
		k, v, _ := strings.Cut(kv, "=")
		args = append(args, "--setenv", k, v)
	}

	if seccompFD >= 0 {
		args = append(args, "--seccomp", strconv.Itoa(seccompFD))
	}
	args = append(args, "--")
	args = append(args, s.Argv...)
	return args, nil
}

func appendSystemMounts(args []string, fsOps jailFS) []string {
	if fsOps.stat != nil {
		if _, err := fsOps.stat("/usr"); err == nil {
			args = append(args, "--ro-bind", "/usr", "/usr")
		}
	}
	for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		if fsOps.lstat == nil {
			continue
		}
		fi, err := fsOps.lstat(dir)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if fsOps.readlink != nil {
				if target, rerr := fsOps.readlink(dir); rerr == nil && target != "" {
					args = append(args, "--symlink", target, dir)
				}
			}
			continue
		}
		if fi.IsDir() {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	return args
}

func appendMinimalEtcMounts(args []string, netMode NetMode, fsOps jailFS) []string {
	if fsOps.stat == nil {
		return args
	}
	paths := baseEtcPaths
	if netMode == NetHost {
		paths = slices.Concat(baseEtcPaths, netHostEtcPaths)
	}
	for _, p := range paths {
		if _, err := fsOps.stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	return args
}

func resolveTargetPath(argv0 string, fsOps jailFS) string {
	if filepath.IsAbs(argv0) {
		return filepath.Clean(argv0)
	}
	if !strings.Contains(argv0, "/") && fsOps.lookPath != nil {
		if p, err := fsOps.lookPath(argv0); err == nil && filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
	}
	return argv0
}

func isSystemPath(p string) bool {
	for _, prefix := range []string{"/usr/", "/bin/", "/sbin/", "/lib/", "/lib64/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

func appendExeBind(args []string, exePath string, seen map[string]bool, fsOps jailFS) []string {
	if !filepath.IsAbs(exePath) || isSystemPath(exePath) {
		return args
	}
	if !seen[exePath] {
		seen[exePath] = true
		args = append(args, "--ro-bind", exePath, exePath)
	}
	if fsOps.evalSymlinks != nil {
		if realPath, err := fsOps.evalSymlinks(exePath); err == nil && filepath.IsAbs(realPath) && !isSystemPath(realPath) && !seen[realPath] {
			seen[realPath] = true
			args = append(args, "--ro-bind", realPath, realPath)
		}
	}
	return args
}

func buildEnvList(extra []string) []string {
	has := map[string]bool{}
	for _, kv := range extra {
		if k, _, ok := strings.Cut(kv, "="); ok {
			has[k] = true
		}
	}
	out := make([]string, 0, 3+len(extra))
	defaults := [][2]string{
		{"PATH", "/usr/local/bin:/usr/bin:/bin"},
		{"HOME", "/tmp"},
		{"LANG", "C.UTF-8"},
	}
	for _, d := range defaults {
		if !has[d[0]] {
			out = append(out, d[0]+"="+d[1])
		}
	}
	out = append(out, extra...)
	return out
}

// bwrapSmokeArgs returns the bubblewrap arguments used to probe whether this
// host can create a user namespace and mount a tmpfs inside it (never reading
// a sysctl).
func bwrapSmokeArgs(trueBin string) []string {
	if trueBin == "" {
		trueBin = "/bin/true"
	}
	return []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-cgroup-try",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
		"--new-session",
		"--", trueBin,
	}
}

func bwrapSupportsDisableUserNS(ver string) bool {
	parts := strings.Split(strings.TrimSpace(ver), ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	minVal, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return maj > 0 || minVal >= 8
}

type userNSProbeDeps struct {
	lookPath func(string) (string, error)
	runCmd   func(ctx context.Context, name string, args ...string) error
}

func probeUserNSWith(ctx context.Context, bwrapPath string, d userNSProbeDeps) bool {
	if bwrapPath == "" {
		if d.lookPath == nil {
			return false
		}
		p, err := d.lookPath("bwrap")
		if err != nil || p == "" {
			return false
		}
		bwrapPath = p
	}
	trueBin := "/bin/true"
	if d.lookPath != nil {
		if p, err := d.lookPath("true"); err == nil && p != "" {
			trueBin = p
		}
	}
	if d.runCmd == nil {
		return false
	}
	return d.runCmd(ctx, bwrapPath, bwrapSmokeArgs(trueBin)...) == nil
}
