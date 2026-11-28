package chv

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ZeroSaltHex is the 32-zero-byte dm-verity salt shared by the tools image and
// user images (isolation spec 20.1, 20.2, 20.3).
const ZeroSaltHex = "0000000000000000000000000000000000000000000000000000000000000000"

const (
	// DefaultVsockCID is the fixed guest vsock CID (spec 20.3).
	DefaultVsockCID = 3
	// DefaultVsockPort is the port guest init listens on (spec 20.3).
	DefaultVsockPort = 1024
	verityBlockBytes = 4096
	sectorsPerBlock  = verityBlockBytes / 512
)

// DiskVerity describes one read-only dm-verity disk passed to the guest kernel.
type DiskVerity struct {
	Path          string
	RootHash      string
	Salt          string // defaults to ZeroSaltHex
	DataBlocks    uint64
	HasSuperblock bool // false for tools (tree at DataBlocks), true for hcsshim user image (tree at DataBlocks+1)
}

// VMConfig holds every parameter needed to construct the Cloud Hypervisor
// command line and guest kernel command line (isolation spec 20.3).
type VMConfig struct {
	VMMPath         string
	CHRemotePath    string
	APISocket       string
	VsockSocket     string
	KernelPath      string
	CPUs            int
	MemoryBytes     int64
	Tools           DiskVerity
	Image           *DiskVerity // nil only for tools-only self-test boot (spec 20.1)
	AgentsDiskPath  string      // optional read-only EROFS/ext4 agents disk (serial=agents)
	SessionDiskPath string      // optional writable ext4 session disk (serial=session)
	SessionDirect   bool        // direct=on on session disk (spec 20.3)
	SerialConsole   bool        // false in normal sessions; true only for boot self-test (spec 20.1)
	InitArgs        []string    // defaults to ["wall-init"]
}

// BuildKernelCmdline builds the guest kernel command line from cfg (spec 20.3).
func BuildKernelCmdline(cfg VMConfig) (string, error) {
	if err := validateVerityDisk("tools", cfg.Tools); err != nil {
		return "", err
	}
	toolsSalt := cfg.Tools.Salt
	if toolsSalt == "" {
		toolsSalt = ZeroSaltHex
	}
	toolsHashStart := cfg.Tools.DataBlocks
	if cfg.Tools.HasSuperblock {
		toolsHashStart++
	}
	tables := make([]string, 0, 2)
	waitfor := make([]string, 0, 2)
	waitfor = append(waitfor, "/dev/vda")
	tables = append(tables, formatVerityTable("tools", 0, "/dev/vda", cfg.Tools.DataBlocks, toolsHashStart, cfg.Tools.RootHash, toolsSalt))

	if cfg.Image != nil {
		if err := validateVerityDisk("image", *cfg.Image); err != nil {
			return "", err
		}
		imgSalt := cfg.Image.Salt
		if imgSalt == "" {
			imgSalt = ZeroSaltHex
		}
		imgHashStart := cfg.Image.DataBlocks
		if cfg.Image.HasSuperblock {
			imgHashStart++
		}
		waitfor = append(waitfor, "/dev/vdb")
		tables = append(tables, formatVerityTable("image", 1, "/dev/vdb", cfg.Image.DataBlocks, imgHashStart, cfg.Image.RootHash, imgSalt))
	}

	initArgs := cfg.InitArgs
	if len(initArgs) == 0 {
		initArgs = []string{"wall-init"}
	}
	for _, a := range initArgs {
		if err := checkSafeToken("init arg", a); err != nil {
			return "", err
		}
	}

	parts := []string{"ro"}
	if cfg.SerialConsole {
		parts = append(parts, "console=ttyS0")
	} else {
		parts = append(parts, "quiet")
	}
	parts = append(parts,
		"root=/dev/dm-0",
		"rootfstype=erofs",
		"init=/init",
		"dm-mod.waitfor="+strings.Join(waitfor, ","),
		fmt.Sprintf("dm-mod.create=%q", strings.Join(tables, ";")),
		"--",
	)
	parts = append(parts, initArgs...)
	return strings.Join(parts, " "), nil
}

func formatVerityTable(name string, minor int, dev string, dataBlocks, hashStart uint64, rootHash, salt string) string {
	sectors := dataBlocks * sectorsPerBlock
	return fmt.Sprintf(
		"%s,,%d,ro,0 %d verity 1 %s %s %d %d %d %d sha256 %s %s 1 panic_on_corruption",
		name, minor, sectors, dev, dev, verityBlockBytes, verityBlockBytes, dataBlocks, hashStart, strings.ToLower(rootHash), strings.ToLower(salt),
	)
}

// BuildArgv constructs the full argv (excluding argv[0]) for Cloud Hypervisor
// v55.0 (isolation spec 20.3). Every disk sets image_type=raw and
// backing_files=off, there is no --net device, and --seccomp true --landlock
// are always enabled.
func BuildArgv(cfg VMConfig) ([]string, error) {
	if err := checkSafePath("api socket", cfg.APISocket); err != nil {
		return nil, err
	}
	if err := checkSafePath("vsock socket", cfg.VsockSocket); err != nil {
		return nil, err
	}
	if err := checkSafePath("kernel", cfg.KernelPath); err != nil {
		return nil, err
	}
	if cfg.CPUs < 1 {
		return nil, fmt.Errorf("cpus must be at least 1, got %d. Try: set cpus >= 1", cfg.CPUs)
	}
	memMiB, err := GuestMemoryMiB(cfg.MemoryBytes)
	if err != nil {
		return nil, err
	}
	cmdline, err := BuildKernelCmdline(cfg)
	if err != nil {
		return nil, err
	}

	disks := make([]string, 0, 4)
	disks = append(disks, rawDiskArg(cfg.Tools.Path, "tools", true, true, false))
	if cfg.Image != nil {
		disks = append(disks, rawDiskArg(cfg.Image.Path, "image", true, true, false))
	}
	if cfg.AgentsDiskPath != "" {
		if err := checkSafePath("agents disk", cfg.AgentsDiskPath); err != nil {
			return nil, err
		}
		disks = append(disks, rawDiskArg(cfg.AgentsDiskPath, "agents", true, true, false))
	}
	if cfg.SessionDiskPath != "" {
		if err := checkSafePath("session disk", cfg.SessionDiskPath); err != nil {
			return nil, err
		}
		disks = append(disks, rawDiskArg(cfg.SessionDiskPath, "session", false, cfg.SessionDirect, true))
	}

	serialMode := "off"
	if cfg.SerialConsole {
		serialMode = "tty"
	}

	argv := make([]string, 0, 20+len(disks))
	argv = append(argv,
		"--api-socket", "path="+cfg.APISocket,
		"--kernel", cfg.KernelPath,
		"--cmdline", cmdline,
		"--cpus", "boot="+strconv.Itoa(cfg.CPUs),
		"--memory", "size="+strconv.FormatInt(memMiB, 10)+"M",
		"--balloon", "size=0,free_page_reporting=on",
		"--disk",
	)
	argv = append(argv, disks...)
	argv = append(argv,
		"--vsock", fmt.Sprintf("cid=%d,socket=%s", DefaultVsockCID, cfg.VsockSocket),
		"--serial", serialMode,
		"--console", "off",
		"--seccomp", "true",
		"--landlock",
	)
	return argv, nil
}

func rawDiskArg(path, serial string, readonly, direct, sparse bool) string {
	b := "path=" + path +
		",readonly=" + onOff(readonly) +
		",direct=" + onOff(direct) +
		",image_type=raw,backing_files=off"
	if sparse {
		b += ",sparse=on"
	}
	return b + ",serial=" + serial
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// WrapBwrap wraps vmmBin and vmmArgv in a minimal bubblewrap confinement shell
// (empty network namespace, private IPC/UTS/PID namespaces, read-only mounts
// for artefacts, writable mounts only for the session and socket directories,
// plus /dev/kvm, /dev/urandom and /dev/null).
func WrapBwrap(cfg VMConfig, vmmBin string, vmmArgv []string) []string {
	args := make([]string, 0, 48+len(vmmArgv))
	args = append(args,
		"--unshare-net",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-pid",
		"--die-with-parent",
		"--new-session",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--dev", "/dev",
		"--dev-bind", "/dev/kvm", "/dev/kvm",
		"--dev-bind", "/dev/urandom", "/dev/urandom",
	)

	seenRO := map[string]bool{}
	bindRO := func(p string) {
		if p == "" || seenRO[p] {
			return
		}
		seenRO[p] = true
		args = append(args, "--ro-bind", p, p)
	}
	bindRO(vmmBin)
	bindRO(cfg.KernelPath)
	bindRO(cfg.Tools.Path)
	if cfg.Image != nil {
		bindRO(cfg.Image.Path)
	}
	bindRO(cfg.AgentsDiskPath)

	seenRW := map[string]bool{}
	bindRWDir := func(p string) {
		if p == "" {
			return
		}
		d := filepath.Dir(p)
		if seenRW[d] {
			return
		}
		seenRW[d] = true
		args = append(args, "--bind", d, d)
	}
	bindRWDir(cfg.APISocket)
	bindRWDir(cfg.VsockSocket)
	if cfg.SessionDiskPath != "" {
		bindRWDir(cfg.SessionDiskPath)
	}

	args = append(args, "--", vmmBin)
	args = append(args, vmmArgv...)
	return args
}

func validateVerityDisk(label string, d DiskVerity) error {
	if err := checkSafePath(label+" disk", d.Path); err != nil {
		return err
	}
	if d.DataBlocks == 0 {
		return fmt.Errorf("%s verity disk has zero data blocks. Try: rebuild or re-pull the %s image", label, label)
	}
	if !isHex64(d.RootHash) {
		return fmt.Errorf("%s verity root hash %q is not 64 hex digits. Try: rebuild or re-pull the %s image", label, d.RootHash, label)
	}
	if d.Salt != "" && !isHex64(d.Salt) {
		return fmt.Errorf("%s verity salt %q is not 64 hex digits. Try: check the image metadata", label, d.Salt)
	}
	return nil
}

func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func checkSafePath(label, p string) error {
	if p == "" {
		return fmt.Errorf("%s path is empty. Try: provide a valid path", label)
	}
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%s path %q must be absolute. Try: pass an absolute path", label, p)
	}
	if strings.ContainsAny(p, ",;\r\n\x00'\"") {
		return fmt.Errorf("%s path %q contains disallowed characters. Try: move the file to a standard path", label, p)
	}
	return nil
}

func checkSafeToken(label, s string) error {
	if s == "" {
		return errors.New("empty " + label + ". Try: pass non-empty arguments")
	}
	if strings.ContainsAny(s, " \t\r\n\x00'\"`;|&") {
		return fmt.Errorf("%s %q contains disallowed whitespace or shell characters. Try: use plain flag tokens", label, s)
	}
	return nil
}
