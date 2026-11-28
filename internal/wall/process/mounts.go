package process

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/brand"
)

//go:embed etc.toml
var etcTOML []byte

// Guest paths inside every process-wall sandbox (isolation spec 7.1).
const (
	GuestHome     = "/home/agent"
	GuestUID      = 1000
	GuestGID      = 1000
	GuestProxyURL = "http://127.0.0.1:3128"
)

// GuestHelperDir is the read-only folder holding the host helper and shadow
// terminal inside a process-wall sandbox.
func GuestHelperDir() string { return "/opt/" + brand.Name + "/bin" }

// GuestRunDir is the private runtime folder inside a process-wall sandbox.
func GuestRunDir() string { return "/run/" + brand.Name }

// Mount describes one bind mount into the sandbox.
type Mount struct {
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// BindData is a small synthetic file mounted read-only into the sandbox via
// bubblewrap's --ro-bind-data flag.
type BindData struct {
	Name string
	Dest string
	Data []byte
}

// Spec describes one process-wall sandbox to launch via bubblewrap.
type Spec struct {
	SessionID      string
	HelperExe      string
	WorkspaceHost  string
	WorkspaceGuest string
	StateHost      string
	StateDirs      []string
	ExtraRO        []Mount
	ExtraRW        []Mount
	GuestPATH      string
	ExtraEnv       []string
	ShareNet       bool
	BwrapPath      string
	// DisableUserNS passes --disable-userns to bwrap when true (supported since
	// bubblewrap 0.8.0). Seccomp also blocks nested user namespaces regardless.
	DisableUserNS bool
}

// Plan is the validated bubblewrap command and confinement plan built from Spec.
type Plan struct {
	BwrapPath   string
	Args        []string
	BindData    []BindData
	Landlock    LandlockConfig
	GuestRunDir string
	GuestHelper string
	StatePaths  []string
}

type etcFile struct {
	Entries []string `toml:"entries"`
}

var (
	etcOnce    sync.Once
	etcEntries []string
	errEtcLoad error
)

// EtcAllowlist returns the sorted allowlist of /etc entries from etc.toml.
func EtcAllowlist() ([]string, error) {
	etcOnce.Do(func() {
		var f etcFile
		if err := toml.Unmarshal(etcTOML, &f); err != nil {
			errEtcLoad = fmt.Errorf("parse etc.toml: %w", err)
			return
		}
		etcEntries = slices.Clone(f.Entries)
	})
	if errEtcLoad != nil {
		return nil, errEtcLoad
	}
	return slices.Clone(etcEntries), nil
}

// refusedStateFiles lists shell startup or hook files that must never be
// persisted across sandbox starts (isolation spec 6.11 and 7.1).
var refusedStateFiles = map[string]bool{
	".bashrc":               true,
	".profile":              true,
	".bash_profile":         true,
	".bash_login":           true,
	".zshrc":                true,
	".zprofile":             true,
	".zshenv":               true,
	".claude/settings.json": true,
}

type planFS struct {
	lstat    func(string) (fs.FileInfo, error)
	stat     func(string) (fs.FileInfo, error)
	readlink func(string) (string, error)
	lookPath func(string) (string, error)
	homeDir  func() (string, error)
}

var defaultPlanFS = planFS{
	lstat:    os.Lstat,
	stat:     os.Stat,
	readlink: os.Readlink,
	lookPath: exec.LookPath,
	homeDir:  os.UserHomeDir,
}

// BuildPlan validates spec and builds the bubblewrap argument list, synthetic
// /etc files and Landlock path rules for a process-wall sandbox.
//
// File descriptor slots reserved by Start():
//   - fd 3: inherited internal/link socketpair (ExtraFiles[0])
//   - fd 4: compiled seccomp-bpf program (--seccomp 4)
//   - fd 5+i: synthetic /etc files (--ro-bind-data <5+i> <dest>)
func BuildPlan(spec Spec) (*Plan, error) {
	return buildPlanWithFS(spec, defaultPlanFS)
}

const (
	seccompFD      = 4
	firstBindDataF = 5
)

func buildPlanWithFS(spec Spec, fsOps planFS) (*Plan, error) {
	if err := validateSpec(spec, fsOps); err != nil {
		return nil, err
	}
	bwrapPath := spec.BwrapPath
	if bwrapPath == "" {
		p, err := fsOps.lookPath("bwrap")
		if err != nil {
			return nil, errors.New("bubblewrap (bwrap) is not installed. Try: install the bubblewrap package on the host (for example: sudo apt install bubblewrap)")
		}
		bwrapPath = p
	}
	etcList, err := EtcAllowlist()
	if err != nil {
		return nil, err
	}

	guestRun := GuestRunDir()
	helperDir := filepath.Dir(spec.HelperExe)
	guestHelper := GuestHelperDir() + "/" + filepath.Base(spec.HelperExe)

	llCfg := buildLandlockConfig(spec, guestRun)
	llJSON, err := EncodeLandlockEnv(llCfg)
	if err != nil {
		return nil, err
	}

	bindData := syntheticEtcFiles(spec.ShareNet, fsOps)
	args := namespaceArgs(spec)
	args = appendSystemMounts(args, fsOps)
	args = appendEtcMounts(args, etcList, bindData, fsOps)
	args = append(args, "--dir", GuestHelperDir(), "--ro-bind", spec.HelperExe, guestHelper)
	vtHost := filepath.Join(helperDir, brand.VTBinary)
	if fi, serr := fsOps.stat(vtHost); serr == nil && !fi.IsDir() {
		args = append(args, "--ro-bind", vtHost, GuestHelperDir()+"/"+brand.VTBinary)
	}
	for _, m := range spec.ExtraRO {
		args = append(args, "--ro-bind", m.Source, m.Dest)
	}

	args, statePaths := appendWritableMounts(args, spec, guestRun)
	args = appendEnvArgs(args, spec, guestRun, llJSON)
	args = append(args, "--seccomp", strconv.Itoa(seccompFD))
	args = append(args, "--", guestHelper, "wall-init")

	return &Plan{
		BwrapPath:   bwrapPath,
		Args:        args,
		BindData:    bindData,
		Landlock:    llCfg,
		GuestRunDir: guestRun,
		GuestHelper: guestHelper,
		StatePaths:  statePaths,
	}, nil
}

func validateSpec(spec Spec, fsOps planFS) error {
	if strings.TrimSpace(spec.SessionID) == "" {
		return errors.New("process wall: missing session ID. Try: start sessions through the host helper")
	}
	if !filepath.IsAbs(spec.HelperExe) {
		return errors.New("process wall: helper executable must be an absolute path. Try: check the host helper installation")
	}
	if !filepath.IsAbs(spec.WorkspaceHost) {
		return errors.New("process wall: host workspace must be an absolute path. Try: pass --dir with a full path")
	}
	cleanHost := filepath.Clean(spec.WorkspaceHost)
	if cleanHost == "/" {
		return errors.New("process wall: refusing to mount / as the workspace. Try: choose a project directory with --dir")
	}
	if home, err := fsOps.homeDir(); err == nil && home != "" && cleanHost == filepath.Clean(home) {
		return errors.New("process wall: refusing to mount the host home folder as the workspace. Try: choose a project directory with --dir")
	}
	if !strings.HasPrefix(spec.WorkspaceGuest, "/work/") || strings.Contains(spec.WorkspaceGuest, "..") {
		return fmt.Errorf("process wall: invalid guest workspace %q. Try: use a path under /work/", spec.WorkspaceGuest)
	}
	for _, rel := range spec.StateDirs {
		if err := validateStateDir(rel); err != nil {
			return err
		}
	}
	if len(spec.StateDirs) > 0 && !filepath.IsAbs(spec.StateHost) {
		return errors.New("process wall: state folder must be an absolute path when state dirs are set. Try: check session state paths")
	}
	for _, m := range append(slices.Clone(spec.ExtraRO), spec.ExtraRW...) {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Dest) {
			return fmt.Errorf("process wall: mount %q -> %q must use absolute paths. Try: use full paths", m.Source, m.Dest)
		}
	}
	return nil
}

func validateStateDir(rel string) error {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("process wall: invalid state dir %q. Try: use a relative path under the guest home", rel)
	}
	if refusedStateFiles[clean] {
		return fmt.Errorf("process wall: refusing to persist startup file %q across starts. Try: keep data folders only", rel)
	}
	return nil
}

func namespaceArgs(spec Spec) []string {
	args := []string{
		"--unshare-user",
		"--unshare-ipc",
		"--unshare-pid",
		"--unshare-uts",
		"--unshare-cgroup-try",
	}
	if !spec.ShareNet {
		args = append(args, "--unshare-net")
	}
	args = append(args,
		"--uid", strconv.Itoa(GuestUID),
		"--gid", strconv.Itoa(GuestGID),
		"--hostname", brand.Name,
		"--cap-drop", "ALL",
		"--die-with-parent",
		"--new-session",
	)
	if spec.DisableUserNS {
		args = append(args, "--disable-userns")
	}
	return args
}

func appendSystemMounts(args []string, fsOps planFS) []string {
	args = append(args, "--proc", "/proc", "--dev", "/dev")
	if _, err := fsOps.stat("/usr"); err == nil {
		args = append(args, "--ro-bind", "/usr", "/usr")
	}
	for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		fi, err := fsOps.lstat(dir)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, rerr := fsOps.readlink(dir); rerr == nil && target != "" {
				args = append(args, "--symlink", target, dir)
			}
			continue
		}
		if fi.IsDir() {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	return args
}

func appendEtcMounts(args, etcList []string, bindData []BindData, fsOps planFS) []string {
	args = append(args, "--dir", "/etc")
	for _, entry := range etcList {
		if entry == "" || strings.Contains(entry, "/") || entry == "." || entry == ".." {
			continue
		}
		p := "/etc/" + entry
		if _, err := fsOps.stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	for i, bd := range bindData {
		args = append(args, "--perms", "0444", "--ro-bind-data", strconv.Itoa(firstBindDataF+i), bd.Dest)
	}
	return args
}

func syntheticEtcFiles(shareNet bool, fsOps planFS) []BindData {
	resolv := []byte("# Direct DNS is disabled in this sandbox; use the HTTP proxy.\n")
	if shareNet {
		if b, err := os.ReadFile("/etc/resolv.conf"); err == nil && len(b) > 0 {
			resolv = b
		}
	}
	_ = fsOps
	return []BindData{
		{
			Name: "etc-passwd",
			Dest: "/etc/passwd",
			Data: []byte("root:x:0:0:root:/root:/bin/sh\nagent:x:1000:1000:agent:/home/agent:/bin/sh\n"),
		},
		{
			Name: "etc-group",
			Dest: "/etc/group",
			Data: []byte("root:x:0:\nagent:x:1000:\n"),
		},
		{
			Name: "etc-hosts",
			Dest: "/etc/hosts",
			Data: []byte("127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n"),
		},
		{
			Name: "etc-resolv.conf",
			Dest: "/etc/resolv.conf",
			Data: resolv,
		},
	}
}

func appendWritableMounts(args []string, spec Spec, guestRun string) ([]string, []string) {
	args = append(args,
		"--tmpfs", "/tmp",
		"--tmpfs", "/var/tmp",
		"--tmpfs", "/run",
		"--perms", "0700", "--dir", guestRun,
		"--perms", "0700", "--tmpfs", GuestHome,
	)
	statePaths := make([]string, 0, len(spec.StateDirs))
	for _, rel := range spec.StateDirs {
		clean := filepath.Clean(rel)
		hostPath := filepath.Join(spec.StateHost, clean)
		guestPath := GuestHome + "/" + filepath.ToSlash(clean)
		statePaths = append(statePaths, hostPath)
		args = append(args, "--bind", hostPath, guestPath)
	}
	args = append(args, "--bind", spec.WorkspaceHost, spec.WorkspaceGuest)
	for _, m := range spec.ExtraRW {
		args = append(args, "--bind", m.Source, m.Dest)
	}
	args = append(args, "--chdir", spec.WorkspaceGuest)
	return args, statePaths
}

func appendEnvArgs(args []string, spec Spec, guestRun, llJSON string) []string {
	guestPATH := spec.GuestPATH
	if guestPATH == "" {
		guestPATH = "/usr/local/bin:/usr/bin:/bin"
	}
	args = append(args,
		"--clearenv",
		"--setenv", "HOME", GuestHome,
		"--setenv", "USER", "agent",
		"--setenv", "LOGNAME", "agent",
		"--setenv", "LANG", "C.UTF-8",
		"--setenv", "PATH", guestPATH,
		"--setenv", brand.Env("GUEST_RUN_DIR"), guestRun,
		"--setenv", LandlockEnvVar(), llJSON,
	)
	for _, kv := range spec.ExtraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			args = append(args, "--setenv", k, v)
		}
	}
	return args
}

func buildLandlockConfig(spec Spec, guestRun string) LandlockConfig {
	rwDirs := make([]string, 0, 5+len(spec.ExtraRW))
	rwDirs = append(rwDirs,
		spec.WorkspaceGuest,
		GuestHome,
		"/tmp",
		"/var/tmp",
		guestRun,
	)
	for _, m := range spec.ExtraRW {
		rwDirs = append(rwDirs, m.Dest)
	}
	return LandlockConfig{
		// The mount namespace already hides host home, host runtime sockets,
		// host /etc secrets and foreign sessions. Allowing read-only on "/"
		// permits directory listings and reading the mounted system/agent trees
		// while blocking every write outside rwDirs and /dev/{pts,shm,null,...}.
		RODirs: []string{"/"},
		RWDirs: rwDirs,
	}
}

// WorkspaceGuestPath returns the guest path under /work/ for a host workspace.
func WorkspaceGuestPath(hostWorkspace string) string {
	base := filepath.Base(filepath.Clean(hostWorkspace))
	var sb strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			sb.WriteRune(r)
		default:
			sb.WriteRune('_')
		}
	}
	clean := strings.TrimLeft(sb.String(), ".")
	if clean == "" {
		clean = "workspace"
	}
	return "/work/" + clean
}
