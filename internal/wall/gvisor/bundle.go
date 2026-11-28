package gvisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/sandbox"
	"github.com/iamlakshya/simstim/internal/wall/process"
)

// Mount describes one host-to-guest bind mount inside the OCI bundle.
type Mount struct {
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Spec describes one gVisor sandbox to prepare and run via upstream runsc.
type Spec struct {
	SessionID      string   `json:"session_id"`
	SessionDir     string   `json:"session_dir"`
	HelperExe      string   `json:"helper_exe"`
	WorkspaceHost  string   `json:"workspace_host"`
	WorkspaceGuest string   `json:"workspace_guest"`
	StateHost      string   `json:"state_host"`
	StateDirs      []string `json:"state_dirs,omitempty"`
	CacheHost      string   `json:"cache_host,omitempty"`
	CacheDirs      []string `json:"cache_dirs,omitempty"`
	ExtraRO        []Mount  `json:"extra_ro,omitempty"`
	ExtraRW        []Mount  `json:"extra_rw,omitempty"`
	GuestPATH      string   `json:"guest_path,omitempty"`
	ExtraEnv       []string `json:"extra_env,omitempty"`
	RunscPath      string   `json:"runsc_path,omitempty"`
	// Command overrides the default [guestHelper, "wall-init"] entrypoint,
	// used by the driver's smoke probe and unit tests.
	Command []string `json:"command,omitempty"`
	// HostUID and HostGID override os.Getuid/os.Getgid in unit tests.
	HostUID int `json:"host_uid,omitempty"`
	HostGID int `json:"host_gid,omitempty"`
}

type bundleFS struct {
	lstat    func(string) (fs.FileInfo, error)
	stat     func(string) (fs.FileInfo, error)
	readlink func(string) (string, error)
	homeDir  func() (string, error)
	getuid   func() int
	getgid   func() int
}

var defaultBundleFS = bundleFS{
	lstat:    os.Lstat,
	stat:     os.Stat,
	readlink: os.Readlink,
	homeDir:  os.UserHomeDir,
	getuid:   os.Getuid,
	getgid:   os.Getgid,
}

// refusedStateFiles matches internal/wall/process/mounts.go so startup/hook
// files never persist across gVisor sandbox restarts either.
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

// BundlePlan holds the generated OCI config, rootfs symlinks, synthetic /etc
// files and host state paths for one gVisor sandbox.
type BundlePlan struct {
	Config       ociSpec
	RootSymlinks map[string]string // name under rootfs/ -> symlink target
	SyntheticEtc map[string][]byte // filename under bundle/etc/ -> bytes
	StatePaths   []string
	CachePaths   []string
	GuestHelper  string
	GuestRunDir  string
}

// BuildBundlePlan validates spec and computes its OCI config and synthetic
// files for bundleDir without touching the filesystem.
func BuildBundlePlan(bundleDir string, spec Spec) (*BundlePlan, error) {
	return buildBundlePlanWithFS(bundleDir, spec, defaultBundleFS)
}

func buildBundlePlanWithFS(bundleDir string, spec Spec, fsOps bundleFS) (*BundlePlan, error) {
	if err := validateSpec(spec, fsOps); err != nil {
		return nil, err
	}
	etcList, err := process.EtcAllowlist()
	if err != nil {
		return nil, err
	}
	guestRun := process.GuestRunDir()
	guestHelper := ""
	if spec.HelperExe != "" {
		guestHelper = process.GuestHelperDir() + "/" + filepath.Base(spec.HelperExe)
	}

	args := spec.Command
	if len(args) == 0 {
		args = []string{guestHelper, "wall-init"}
	}

	hostUID := spec.HostUID
	if hostUID == 0 && fsOps.getuid != nil {
		hostUID = fsOps.getuid()
	}
	hostGID := spec.HostGID
	if hostGID == 0 && fsOps.getgid != nil {
		hostGID = fsOps.getgid()
	}

	synthEtc := syntheticEtcFiles()
	etcDir := filepath.Join(bundleDir, "etc")
	mounts, rootLinks := buildSystemAndEtcMounts(etcList, synthEtc, etcDir, fsOps)
	mounts = appendHelperAndExtraROMounts(mounts, spec, guestHelper, fsOps)
	mounts, statePaths, cachePaths := appendWritableMounts(mounts, spec, guestRun)

	cfg := ociSpec{
		OCIVersion: "1.0.2",
		Process: ociProcess{
			Terminal: false,
			User:     ociUser{UID: process.GuestUID, GID: process.GuestGID},
			Args:     slices.Clone(args),
			Env:      buildEnv(spec, guestRun),
			Cwd:      spec.WorkspaceGuest,
			Capabilities: &ociCapabilities{
				Bounding:    []string{},
				Effective:   []string{},
				Inheritable: []string{},
				Permitted:   []string{},
				Ambient:     []string{},
			},
			NoNewPrivileges: true,
		},
		Root:     ociRoot{Path: "rootfs", ReadOnly: true},
		Hostname: brand.Name,
		Mounts:   mounts,
		Linux: ociLinux{
			UIDMappings: []ociIDMapping{{
				ContainerID: process.GuestUID,
				HostID:      uint32(max(hostUID, 0)), //nolint:gosec // G115: non-negative uid clamped via max
				Size:        1,
			}},
			GIDMappings: []ociIDMapping{{
				ContainerID: process.GuestGID,
				HostID:      uint32(max(hostGID, 0)), //nolint:gosec // G115: non-negative gid clamped via max
				Size:        1,
			}},
			Namespaces: []ociNamespace{
				{Type: "user"},
				{Type: "pid"},
				{Type: "ipc"},
				{Type: "uts"},
				{Type: "mount"},
			},
			Seccomp: buildOCISeccomp(),
			MaskedPaths: []string{
				"/proc/kcore",
				"/proc/keys",
				"/proc/timer_list",
				"/proc/sched_debug",
				"/sys/firmware",
			},
			ReadonlyPaths: []string{
				"/proc/asound",
				"/proc/bus",
				"/proc/fs",
				"/proc/irq",
				"/proc/sys",
				"/proc/sysrq-trigger",
			},
		},
	}

	return &BundlePlan{
		Config:       cfg,
		RootSymlinks: rootLinks,
		SyntheticEtc: synthEtc,
		StatePaths:   statePaths,
		CachePaths:   cachePaths,
		GuestHelper:  guestHelper,
		GuestRunDir:  guestRun,
	}, nil
}

// PrepareBundle writes bundleDir/rootfs, bundleDir/etc and bundleDir/config.json
// for spec and ensures every host StateDir exists.
func PrepareBundle(bundleDir string, spec Spec) (*BundlePlan, error) {
	plan, err := BuildBundlePlan(bundleDir, spec)
	if err != nil {
		return nil, err
	}
	for _, dir := range plan.StatePaths {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("prepare session state folder %s: %w", dir, err)
		}
	}
	for _, dir := range plan.CachePaths {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return nil, fmt.Errorf("prepare session cache folder %s: %w", dir, err)
		}
	}
	rootfsDir := filepath.Join(bundleDir, "rootfs")
	etcDir := filepath.Join(bundleDir, "etc")
	for _, dir := range []string{bundleDir, rootfsDir, etcDir} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	for name, target := range plan.RootSymlinks {
		linkPath := filepath.Join(rootfsDir, name)
		_ = os.Remove(linkPath)
		if err := os.Symlink(target, linkPath); err != nil {
			return nil, fmt.Errorf("create rootfs symlink %s -> %s: %w", name, target, err)
		}
	}
	etcRoot, err := os.OpenRoot(etcDir)
	if err != nil {
		return nil, fmt.Errorf("open bundle etc dir: %w", err)
	}
	defer etcRoot.Close()
	for name, data := range plan.SyntheticEtc {
		if err := etcRoot.WriteFile(name, data, 0o444); err != nil {
			return nil, fmt.Errorf("write synthetic /etc/%s: %w", name, err)
		}
	}

	b, err := json.MarshalIndent(plan.Config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode OCI config.json: %w", err)
	}
	bundleRoot, err := os.OpenRoot(bundleDir)
	if err != nil {
		return nil, fmt.Errorf("open bundle dir: %w", err)
	}
	defer bundleRoot.Close()
	if err := bundleRoot.WriteFile("config.json", append(b, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write OCI config.json: %w", err)
	}
	return plan, nil
}

func validateSpec(spec Spec, fsOps bundleFS) error {
	if strings.TrimSpace(spec.SessionID) == "" {
		return errors.New("gvisor wall: missing session ID. Try: start sessions through the host helper")
	}
	if len(spec.Command) == 0 && !filepath.IsAbs(spec.HelperExe) {
		return errors.New("gvisor wall: helper executable must be an absolute path. Try: check the host helper installation")
	}
	if !filepath.IsAbs(spec.WorkspaceHost) {
		return errors.New("gvisor wall: host workspace must be an absolute path. Try: pass --dir with a full path")
	}
	cleanHost := filepath.Clean(spec.WorkspaceHost)
	if cleanHost == "/" {
		return errors.New("gvisor wall: refusing to mount / as the workspace. Try: choose a project directory with --dir")
	}
	if fsOps.homeDir != nil {
		if home, err := fsOps.homeDir(); err == nil && home != "" && cleanHost == filepath.Clean(home) {
			return errors.New("gvisor wall: refusing to mount the host home folder as the workspace. Try: choose a project directory with --dir")
		}
	}
	if !strings.HasPrefix(spec.WorkspaceGuest, "/work/") || strings.Contains(spec.WorkspaceGuest, "..") {
		return fmt.Errorf("gvisor wall: invalid guest workspace %q. Try: use a path under /work/", spec.WorkspaceGuest)
	}
	for _, rel := range spec.StateDirs {
		if err := validateStateDir(rel); err != nil {
			return err
		}
	}
	if len(spec.StateDirs) > 0 && !filepath.IsAbs(spec.StateHost) {
		return errors.New("gvisor wall: state folder must be an absolute path when state dirs are set. Try: check session state paths")
	}
	for _, rel := range spec.CacheDirs {
		if _, err := sandbox.ValidateCachePath(rel); err != nil {
			return fmt.Errorf("gvisor wall: %w", err)
		}
	}
	if len(spec.CacheDirs) > 0 && !filepath.IsAbs(spec.CacheHost) {
		return errors.New("gvisor wall: cache folder must be an absolute path when cache dirs are set. Try: check session cache paths")
	}
	for _, m := range append(slices.Clone(spec.ExtraRO), spec.ExtraRW...) {
		if !filepath.IsAbs(m.Source) || !filepath.IsAbs(m.Dest) {
			return fmt.Errorf("gvisor wall: mount %q -> %q must use absolute paths. Try: use full paths", m.Source, m.Dest)
		}
	}
	return nil
}

func validateStateDir(rel string) error {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("gvisor wall: invalid state dir %q. Try: use a relative path under the guest home", rel)
	}
	if refusedStateFiles[clean] {
		return fmt.Errorf("gvisor wall: refusing to persist startup file %q across starts. Try: keep data folders only", rel)
	}
	return nil
}

func syntheticEtcFiles() map[string][]byte {
	return map[string][]byte{
		"passwd":      []byte("root:x:0:0:root:/root:/bin/sh\nagent:x:1000:1000:agent:/home/agent:/bin/sh\n"),
		"group":       []byte("root:x:0:\nagent:x:1000:\n"),
		"hosts":       []byte("127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n"),
		"resolv.conf": []byte("# Direct DNS is disabled in this sandbox; use the HTTP proxy.\n"),
	}
}

func buildSystemAndEtcMounts(etcList []string, synthEtc map[string][]byte, etcDir string, fsOps bundleFS) ([]ociMount, map[string]string) {
	mounts := []ociMount{
		{Destination: "/proc", Type: "proc", Source: "proc"},
		{Destination: "/dev", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "strictatime", "mode=755", "size=65536k"}},
		{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"}},
		{Destination: "/dev/shm", Type: "tmpfs", Source: "shm", Options: []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"}},
		{Destination: "/sys", Type: "sysfs", Source: "sysfs", Options: []string{"nosuid", "noexec", "nodev", "ro"}},
	}
	if _, err := fsOps.stat("/usr"); err == nil {
		mounts = append(mounts, ociMount{Destination: "/usr", Type: "bind", Source: "/usr", Options: []string{"rbind", "ro"}})
	}
	rootLinks := map[string]string{}
	for _, dir := range []string{"/bin", "/sbin", "/lib", "/lib64"} {
		fi, err := fsOps.lstat(dir)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, rerr := fsOps.readlink(dir); rerr == nil && target != "" {
				rootLinks[strings.TrimPrefix(dir, "/")] = target
			}
			continue
		}
		if fi.IsDir() {
			mounts = append(mounts, ociMount{Destination: dir, Type: "bind", Source: dir, Options: []string{"rbind", "ro"}})
		}
	}

	mounts = append(mounts, ociMount{Destination: "/etc", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=0755"}})
	for _, entry := range etcList {
		if entry == "" || strings.Contains(entry, "/") || entry == "." || entry == ".." {
			continue
		}
		if _, isSynth := synthEtc[entry]; isSynth {
			continue
		}
		p := "/etc/" + entry
		if _, err := fsOps.stat(p); err == nil {
			mounts = append(mounts, ociMount{Destination: p, Type: "bind", Source: p, Options: []string{"rbind", "ro"}})
		}
	}
	for _, name := range []string{"passwd", "group", "hosts", "resolv.conf"} {
		mounts = append(mounts, ociMount{
			Destination: "/etc/" + name,
			Type:        "bind",
			Source:      filepath.Join(etcDir, name),
			Options:     []string{"rbind", "ro"},
		})
	}
	return mounts, rootLinks
}

func appendHelperAndExtraROMounts(mounts []ociMount, spec Spec, guestHelper string, fsOps bundleFS) []ociMount {
	if spec.HelperExe != "" && guestHelper != "" {
		mounts = append(mounts, ociMount{
			Destination: guestHelper,
			Type:        "bind",
			Source:      spec.HelperExe,
			Options:     []string{"rbind", "ro"},
		})
		vtHost := filepath.Join(filepath.Dir(spec.HelperExe), brand.VTBinary)
		if fi, err := fsOps.stat(vtHost); err == nil && !fi.IsDir() {
			mounts = append(mounts, ociMount{
				Destination: process.GuestHelperDir() + "/" + brand.VTBinary,
				Type:        "bind",
				Source:      vtHost,
				Options:     []string{"rbind", "ro"},
			})
		}
	}
	for _, m := range spec.ExtraRO {
		mounts = append(mounts, ociMount{
			Destination: m.Dest,
			Type:        "bind",
			Source:      m.Source,
			Options:     []string{"rbind", "ro"},
		})
	}
	return mounts
}

func appendWritableMounts(mounts []ociMount, spec Spec, guestRun string) (outMounts []ociMount, statePaths, cachePaths []string) {
	uidOpt := "uid=" + strconv.Itoa(process.GuestUID)
	gidOpt := "gid=" + strconv.Itoa(process.GuestGID)
	mounts = append(mounts,
		ociMount{Destination: "/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777"}},
		ociMount{Destination: "/var/tmp", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=1777"}},
		ociMount{Destination: "/run", Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=0700", uidOpt, gidOpt}},
		ociMount{Destination: guestRun, Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=0700", uidOpt, gidOpt}},
		ociMount{Destination: process.GuestHome, Type: "tmpfs", Source: "tmpfs", Options: []string{"nosuid", "nodev", "mode=0700", uidOpt, gidOpt}},
	)
	mounted := map[string]bool{}
	statePaths = make([]string, 0, len(spec.StateDirs))
	for _, rel := range spec.StateDirs {
		clean := filepath.Clean(rel)
		slash := filepath.ToSlash(clean)
		mounted[slash] = true
		hostPath := filepath.Join(spec.StateHost, clean)
		guestPath := process.GuestHome + "/" + slash
		statePaths = append(statePaths, hostPath)
		mounts = append(mounts, ociMount{
			Destination: guestPath,
			Type:        "bind",
			Source:      hostPath,
			Options:     []string{"rbind", "rw"},
		})
	}
	cachePaths = make([]string, 0, len(spec.CacheDirs))
	for _, rel := range sandbox.SortMountOrder(spec.CacheDirs) {
		clean := filepath.Clean(rel)
		slash := filepath.ToSlash(clean)
		if mounted[slash] {
			continue
		}
		mounted[slash] = true
		hostPath := filepath.Join(spec.CacheHost, clean)
		guestPath := process.GuestHome + "/" + slash
		cachePaths = append(cachePaths, hostPath)
		mounts = append(mounts, ociMount{
			Destination: guestPath,
			Type:        "bind",
			Source:      hostPath,
			Options:     []string{"rbind", "rw"},
		})
	}
	mounts = append(mounts, ociMount{
		Destination: spec.WorkspaceGuest,
		Type:        "bind",
		Source:      spec.WorkspaceHost,
		Options:     []string{"rbind", "rw"},
	})
	for _, m := range spec.ExtraRW {
		mounts = append(mounts, ociMount{
			Destination: m.Dest,
			Type:        "bind",
			Source:      m.Source,
			Options:     []string{"rbind", "rw"},
		})
	}
	return mounts, statePaths, cachePaths
}

func buildEnv(spec Spec, guestRun string) []string {
	guestPATH := spec.GuestPATH
	if guestPATH == "" {
		guestPATH = "/usr/local/bin:/usr/bin:/bin"
	}
	env := []string{
		"HOME=" + process.GuestHome,
		"USER=agent",
		"LOGNAME=agent",
		"LANG=C.UTF-8",
		"PATH=" + guestPATH,
		brand.Env("GUEST_RUN_DIR") + "=" + guestRun,
	}
	for _, kv := range spec.ExtraEnv {
		if k, _, ok := strings.Cut(kv, "="); ok && k != "" {
			env = append(env, kv)
		}
	}
	return env
}
