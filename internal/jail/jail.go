// Package jail confines a trusted-side process that handles untrusted input,
// such as a VMM, an image converter, host-side git or the egress process
// (isolation spec 5.6, decision 0053).
//
// On Linux hosts where bubblewrap and unprivileged user namespaces work, jails
// run under bubblewrap with mount, PID, IPC, UTS, cgroup-try and (for NetNone)
// network namespaces, zero capabilities, no_new_privs and a go-seccomp-bpf
// filter. Where user namespaces are unavailable, jails fall back to Landlock
// (go-landlock, requiring ABI 3 or later) and seccomp through the helper's
// internal jail-exec trampoline. Neither mode ever calls setgroups, so a vmm
// jail keeps supplementary groups such as kvm.
package jail

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// ExecSubcommand is the internal host-helper subcommand used to apply Landlock
// and seccomp before execve(2) when running without a user namespace (or when
// explicit Landlock rules are layered inside bubblewrap).
const ExecSubcommand = "jail-exec"

// MinNoUserNSLandlockABI is the minimum Landlock ABI required when a jail runs
// without a user namespace (Linux 6.2, ABI 3, needed to refuse truncate(2);
// isolation spec 5.6).
const MinNoUserNSLandlockABI = 3

// NetMode chooses network isolation for a jailed command.
type NetMode int

const (
	// NetNone isolates the command in an empty network namespace (and refuses
	// AF_INET/AF_INET6 via seccomp).
	NetNone NetMode = iota
	// NetHost retains the host's network namespace (used by the egress process).
	NetHost
)

// BindMode chooses how a host path is mounted into a bubblewrap jail.
type BindMode int

const (
	// BindRO mounts the host path read-only (--ro-bind).
	BindRO BindMode = iota
	// BindRW mounts the host path read-write (--bind).
	BindRW
	// BindDev mounts a device node with device access (--dev-bind).
	BindDev
)

// Bind describes one host path exposed inside the jail's mount namespace.
// Dest defaults to Src when empty.
type Bind struct {
	Src  string   `json:"src"`
	Dest string   `json:"dest,omitempty"`
	Mode BindMode `json:"mode,omitempty"`
}

// PathRule describes one Landlock filesystem rule.
type PathRule struct {
	Path     string `json:"path"`
	Write    bool   `json:"write,omitempty"`
	IoctlDev bool   `json:"ioctl_dev,omitempty"`
}

// Profile selects the seccomp profile applied to a jailed command.
type Profile string

// Built-in jail seccomp profiles (isolation spec 5.6).
const (
	ProfileVMM       Profile = "vmm"
	ProfileConverter Profile = "converter"
	ProfileGit       Profile = "git"
	ProfileEgress    Profile = "egress"
)

// Spec describes how to confine one trusted-side command.
type Spec struct {
	Argv    []string   // command and arguments to run (Argv[0] required)
	Env     []string   // explicit allowlisted environment (KEY=VAL); host env is cleared
	Dir     string     // optional working directory inside the jail
	Net     NetMode    // NetNone (default): no network. NetHost: host network.
	Binds   []Bind     // the only non-system host paths visible in the jail
	Rules   []PathRule // extra Landlock rules (applied in no-userns mode and when set inside bwrap)
	Seccomp Profile    // "vmm", "converter", "git", "egress" (empty uses strict default)
	Files   []*os.File // inherited starting from fd 3

	// Optional overrides for callers and tests.
	BwrapPath  string // bubblewrap binary path (defaults to LookPath("bwrap"))
	ExecHelper string // helper binary serving ExecSubcommand (defaults to os.Executable())
}

// Command returns an *exec.Cmd that starts s.Argv inside the jail. Every jail
// sets no_new_privs and drops all capabilities before running s.Argv, and never
// calls setgroups.
//
// Any internal descriptors appended to cmd.ExtraFiles past len(s.Files) (such
// as the seccomp BPF memfd) can be closed in the parent after cmd.Start() by
// calling CloseInternalFiles(cmd, len(s.Files)).
func Command(ctx context.Context, s Spec) (*exec.Cmd, error) {
	return commandWithDeps(ctx, s, defaultDeps())
}

// CloseInternalFiles closes internal file descriptors appended to
// cmd.ExtraFiles by Command (those at index >= userFileCount), leaving the
// caller's own Spec.Files untouched. Call it in the parent after cmd.Start().
func CloseInternalFiles(cmd *exec.Cmd, userFileCount int) {
	if cmd == nil || userFileCount < 0 || userFileCount >= len(cmd.ExtraFiles) {
		return
	}
	for _, f := range cmd.ExtraFiles[userFileCount:] {
		if f != nil {
			_ = f.Close()
		}
	}
}

// Userns reports whether jails on this host get a user namespace through
// bubblewrap. It probes by creating a user namespace and mounting a tmpfs in
// it rather than reading a sysctl.
func Userns() bool {
	return defaultUserns()
}

// VMMSpec builds a jail Spec for a VMM process (such as Cloud Hypervisor):
// no network, /dev/kvm device bind, read-only artefact paths and read-write
// socket/session directories.
func VMMSpec(argv, roPaths, rwDirs []string) Spec {
	binds := make([]Bind, 0, 1+len(roPaths)+len(rwDirs))
	binds = append(binds, Bind{Src: "/dev/kvm", Dest: "/dev/kvm", Mode: BindDev})
	for _, p := range roPaths {
		if p != "" {
			binds = append(binds, Bind{Src: p, Dest: p, Mode: BindRO})
		}
	}
	for _, d := range rwDirs {
		if d != "" {
			binds = append(binds, Bind{Src: d, Dest: d, Mode: BindRW})
		}
	}
	return Spec{
		Argv:    slices.Clone(argv),
		Net:     NetNone,
		Binds:   binds,
		Seccomp: ProfileVMM,
	}
}

// ConverterSpec builds a jail Spec for an image converter: no network,
// read-only inputs and read-write output paths.
func ConverterSpec(argv, roPaths, rwPaths []string) Spec {
	binds := make([]Bind, 0, len(roPaths)+len(rwPaths))
	for _, p := range roPaths {
		if p != "" {
			binds = append(binds, Bind{Src: p, Dest: p, Mode: BindRO})
		}
	}
	for _, p := range rwPaths {
		if p != "" {
			binds = append(binds, Bind{Src: p, Dest: p, Mode: BindRW})
		}
	}
	return Spec{
		Argv:    slices.Clone(argv),
		Net:     NetNone,
		Binds:   binds,
		Seccomp: ProfileConverter,
	}
}

// GitSpec builds a jail Spec for a host-side git command: no network by
// default, explicit repository/output binds and the git seccomp profile.
func GitSpec(argv []string, dir string, roPaths, rwPaths []string) Spec {
	binds := make([]Bind, 0, len(roPaths)+len(rwPaths))
	for _, p := range roPaths {
		if p != "" {
			binds = append(binds, Bind{Src: p, Dest: p, Mode: BindRO})
		}
	}
	for _, p := range rwPaths {
		if p != "" {
			binds = append(binds, Bind{Src: p, Dest: p, Mode: BindRW})
		}
	}
	return Spec{
		Argv:    slices.Clone(argv),
		Dir:     dir,
		Net:     NetNone,
		Binds:   binds,
		Seccomp: ProfileGit,
	}
}

// EgressSpec builds a jail Spec for the session egress process: host network
// (with TLS roots and DNS config mounted read-only), inherited link files
// starting at fd 3 and the egress seccomp profile.
func EgressSpec(argv []string, files []*os.File) Spec {
	return Spec{
		Argv:    slices.Clone(argv),
		Net:     NetHost,
		Seccomp: ProfileEgress,
		Files:   slices.Clone(files),
	}
}

type jailFS struct {
	lstat        func(string) (fs.FileInfo, error)
	stat         func(string) (fs.FileInfo, error)
	readlink     func(string) (string, error)
	evalSymlinks func(string) (string, error)
	lookPath     func(string) (string, error)
	homeDir      func() (string, error)
	getenv       func(string) string
}

var defaultJailFS = jailFS{
	lstat:        os.Lstat,
	stat:         os.Stat,
	readlink:     os.Readlink,
	evalSymlinks: filepath.EvalSymlinks,
	lookPath:     exec.LookPath,
	homeDir:      os.UserHomeDir,
	getenv:       os.Getenv,
}

func validateSpec(s Spec, fsOps jailFS) error {
	if len(s.Argv) == 0 || strings.TrimSpace(s.Argv[0]) == "" {
		return errors.New("jail: missing command argv. Try: pass the command and arguments in Spec.Argv")
	}
	if !validProfile(s.Seccomp) {
		return fmt.Errorf("jail: unknown seccomp profile %q. Try: use vmm, converter, git or egress", s.Seccomp)
	}
	if s.Net != NetNone && s.Net != NetHost {
		return fmt.Errorf("jail: unknown network mode %d. Try: use NetNone or NetHost", s.Net)
	}
	if (s.Seccomp == ProfileVMM || s.Seccomp == ProfileConverter) && s.Net == NetHost {
		return fmt.Errorf("jail: %s profile cannot use host network. Try: set Net to NetNone", s.Seccomp)
	}
	if s.Dir != "" && !filepath.IsAbs(s.Dir) {
		return fmt.Errorf("jail: working directory %q must be an absolute path. Try: pass an absolute directory", s.Dir)
	}

	home, runtimeDir := hostForbiddenRoots(fsOps)
	if err := validateBinds(s.Binds, home, runtimeDir); err != nil {
		return err
	}
	return validateRulesAndEnv(s.Rules, s.Env, home, runtimeDir)
}

func hostForbiddenRoots(fsOps jailFS) (home, runtimeDir string) {
	if fsOps.homeDir != nil {
		if h, err := fsOps.homeDir(); err == nil && h != "" {
			home = filepath.Clean(h)
		}
	}
	if fsOps.getenv != nil {
		if rd := strings.TrimSpace(fsOps.getenv("XDG_RUNTIME_DIR")); rd != "" {
			runtimeDir = filepath.Clean(rd)
		}
	}
	return home, runtimeDir
}

func validateBinds(binds []Bind, home, runtimeDir string) error {
	for _, b := range binds {
		if !filepath.IsAbs(b.Src) {
			return fmt.Errorf("jail: bind source %q must be an absolute path. Try: pass an absolute host path", b.Src)
		}
		if b.Dest != "" && !filepath.IsAbs(b.Dest) {
			return fmt.Errorf("jail: bind destination %q must be an absolute path. Try: pass an absolute path", b.Dest)
		}
		if b.Mode != BindRO && b.Mode != BindRW && b.Mode != BindDev {
			return fmt.Errorf("jail: unknown bind mode %d for %q. Try: use BindRO, BindRW or BindDev", b.Mode, b.Src)
		}
		if err := checkForbiddenPath(filepath.Clean(b.Src), home, runtimeDir); err != nil {
			return err
		}
	}
	return nil
}

func validateRulesAndEnv(rules []PathRule, env []string, home, runtimeDir string) error {
	for _, r := range rules {
		if !filepath.IsAbs(r.Path) {
			return fmt.Errorf("jail: Landlock rule path %q must be an absolute path. Try: pass an absolute path", r.Path)
		}
		if err := checkForbiddenPath(filepath.Clean(r.Path), home, runtimeDir); err != nil {
			return err
		}
	}
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.ContainsRune(kv, '\x00') {
			return fmt.Errorf("jail: invalid environment entry %q. Try: pass KEY=VALUE entries with no NUL bytes", kv)
		}
	}
	return nil
}

func checkForbiddenPath(clean, home, runtimeDir string) error {
	if clean == "/" {
		return errors.New("jail: refusing to expose / in a jail. Try: bind only the specific files or directories the command needs")
	}
	if home != "" && clean == home {
		return errors.New("jail: refusing to expose the host home folder in a jail. Try: bind only the specific files or subdirectories needed")
	}
	if runtimeDir != "" && clean == runtimeDir {
		return errors.New("jail: refusing to expose XDG_RUNTIME_DIR in a jail. Try: bind only a private session socket subdirectory")
	}
	if isVhostDevice(clean) {
		return fmt.Errorf("jail: refusing to expose vhost device %q in a jail. Try: remove /dev/vhost-* from jail binds", clean)
	}
	return nil
}

func isVhostDevice(clean string) bool {
	return clean == "/dev/vhost" || strings.HasPrefix(clean, "/dev/vhost-")
}

func validProfile(p Profile) bool {
	switch p {
	case "", ProfileVMM, ProfileConverter, ProfileGit, ProfileEgress:
		return true
	default:
		return false
	}
}

func checkNoUserNSLandlockABI(abi int) error {
	if abi < MinNoUserNSLandlockABI {
		return fmt.Errorf("jail requires a user namespace or Landlock ABI %d or later (this kernel has ABI %d). Try: install bubblewrap and allow unprivileged user namespaces (host doctor), or boot Linux 6.2 or later", MinNoUserNSLandlockABI, abi)
	}
	return nil
}

func checkNoUserNSBinds(binds []Bind) error {
	for _, b := range binds {
		if b.Dest != "" && filepath.Clean(b.Dest) != filepath.Clean(b.Src) {
			return fmt.Errorf("jail: cannot remap bind %q -> %q without a user namespace. Try: use matching source and destination paths, or enable bubblewrap user namespaces", b.Src, b.Dest)
		}
	}
	return nil
}
