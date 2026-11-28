package chv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/wall"
)

const devKVMPath = "/dev/kvm"

// ProbeDeps injects host checks so ProbeWith runs cleanly on any OS in unit tests.
type ProbeDeps struct {
	GOOS          string
	GOARCH        string
	KernelRelease func() string
	Stat          func(string) (os.FileInfo, error)
	CheckKVMRW    func(string) error
	LookPath      func(string) (string, error)
	RuntimePaths  func() RuntimePaths
	CurrentUser   func() string
	RunCmd        func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func defaultProbeDeps() ProbeDeps {
	return ProbeDeps{
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		KernelRelease: kernelRelease,
		Stat:          os.Stat,
		CheckKVMRW:    checkKVMRW,
		LookPath:      exec.LookPath,
		RuntimePaths:  DefaultRuntimePaths,
		CurrentUser: func() string {
			if u, err := user.Current(); err == nil && u != nil && u.Username != "" {
				return u.Username
			}
			return "$USER"
		},
		RunCmd: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
			if err != nil {
				return out, fmt.Errorf("run %s: %w", name, err)
			}
			return out, nil
		},
	}
}

// ProbeWith checks whether this host can boot Cloud Hypervisor microvms
// (isolation spec 20.3, 20.11).
func ProbeWith(ctx context.Context, d ProbeDeps) wall.Probe {
	facts := map[string]string{}
	p := wall.Probe{
		Driver: DriverName,
		Level:  wall.MicroVM,
		Facts:  facts,
	}
	if d.GOOS != "linux" {
		p.Reason = "microvm walls with Cloud Hypervisor require Linux with KVM"
		p.Fix = "use a Linux host with /dev/kvm, or a Lima VM with nested virtualisation on macOS"
		return p
	}
	if _, err := PinsForArch(d.GOARCH); err != nil {
		p.Reason = fmt.Sprintf("architecture %s is not supported by Cloud Hypervisor", d.GOARCH)
		p.Fix = "use an amd64 or arm64 Linux host"
		return p
	}

	release := ""
	if d.KernelRelease != nil {
		release = strings.TrimSpace(d.KernelRelease())
	}
	if release != "" {
		facts["kernel"] = release
	}
	if kernelOlderThan68(release) {
		p.Reason = fmt.Sprintf("host kernel %s is older than Linux 6.8 (Cloud Hypervisor minimum)", release)
		p.Fix = "upgrade the host kernel to Linux 6.8 or later"
		return p
	}

	if d.Stat == nil {
		d.Stat = os.Stat
	}
	if !probeKVM(&p, d) {
		return p
	}

	mkfsPath, mkfsErr := FindMkfsExt4(d.LookPath, d.Stat)
	if mkfsErr != nil {
		facts["mkfs_ext4"] = "missing"
		p.Reason = "mkfs.ext4 is not installed (needed to format session disks)"
		p.Fix = "sudo sh -c 'apt-get update && apt-get install -y e2fsprogs'"
		return p
	}
	facts["mkfs_ext4"] = mkfsPath

	rp, ok := probeRuntimeFiles(&p, d)
	if !ok {
		return p
	}
	p.Version = probeVMMVersion(ctx, d, rp.VMM, facts)
	p.OK = true
	return p
}

func probeKVM(p *wall.Probe, d ProbeDeps) bool {
	if _, err := d.Stat(devKVMPath); err != nil {
		p.Facts["kvm"] = "absent"
		p.Reason = "the microvm wall needs /dev/kvm, and this host has none"
		p.Fix = "enable KVM or nested virtualisation on this host, or use the process wall"
		return false
	}
	p.Facts["kvm"] = "present"
	if d.CheckKVMRW != nil {
		if err := d.CheckKVMRW(devKVMPath); err != nil {
			p.Facts["kvm_usable"] = "false"
			username := ""
			if d.CurrentUser != nil {
				username = d.CurrentUser()
			}
			displayUser := username
			if displayUser == "" {
				displayUser = "$USER"
			}
			p.Reason = fmt.Sprintf("/dev/kvm is not accessible to %s (%v)", displayUser, err)
			p.Fix = kvmAccessFix(username, hasBin(d.LookPath, "setfacl"))
			return false
		}
	}
	p.Facts["kvm_usable"] = "true"
	return true
}

func probeRuntimeFiles(p *wall.Probe, d ProbeDeps) (RuntimePaths, bool) {
	rp := RuntimePaths{}
	if d.RuntimePaths != nil {
		rp = d.RuntimePaths()
	}
	for _, item := range []struct{ key, label, path string }{
		{"vmm", "cloud-hypervisor", rp.VMM},
		{"ch_remote", "ch-remote", rp.CHRemote},
		{"kernel", "guest kernel (vmlinux)", rp.Kernel},
	} {
		if item.path == "" || !pathExists(d.Stat, item.path) {
			p.Facts[item.key] = "missing"
			p.Reason = fmt.Sprintf("%s is not installed in %s", item.label, rp.Dir)
			p.Fix = fmt.Sprintf("install the pinned Cloud Hypervisor v%s and Kata %s kernel runtime into %s", PinnedVMMVersion, PinnedKernelVersion, rp.Dir)
			return rp, false
		}
		p.Facts[item.key] = item.path
	}
	return rp, true
}

func probeVMMVersion(ctx context.Context, d ProbeDeps, vmmPath string, facts map[string]string) string {
	if d.RunCmd != nil {
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		out, err := d.RunCmd(tctx, vmmPath, "--version")
		cancel()
		if err == nil {
			if ver := parseCHVVersion(string(out)); ver != "" {
				facts["vmm_version"] = ver
				return ver
			}
		}
	}
	return PinnedVMMVersion
}

func kernelOlderThan68(release string) bool {
	maj, minVal, ok := api.ParseKernelMajorMinor(release)
	return ok && (maj < 6 || (maj == 6 && minVal < 8))
}

func parseCHVVersion(out string) string {
	fields := strings.Fields(out)
	for i, f := range fields {
		if strings.EqualFold(f, "cloud-hypervisor") && i+1 < len(fields) {
			return strings.TrimPrefix(fields[i+1], "v")
		}
	}
	if len(fields) == 1 {
		return strings.TrimPrefix(fields[0], "v")
	}
	return ""
}

func pathExists(stat func(string) (os.FileInfo, error), p string) bool {
	if stat == nil || p == "" {
		return false
	}
	fi, err := stat(p)
	return err == nil && !fi.IsDir()
}

func hasBin(lookPath func(string) (string, error), name string) bool {
	if lookPath == nil {
		return false
	}
	p, err := lookPath(name)
	return err == nil && p != ""
}

// kvmAccessFix builds a single-sudo shell hint for granting /dev/kvm access.
// When username is empty, "$USER" stays outside single quotes so the caller's
// shell expands it; known usernames are shell-quoted to prevent injection.
func kvmAccessFix(username string, hasSetfacl bool) string {
	arg := `"$USER"`
	if username != "" {
		arg = shellArg(username)
	}
	if hasSetfacl {
		return `sudo sh -c 'setfacl -m "u:$1:rw" /dev/kvm && usermod -aG kvm "$1"' _ ` + arg
	}
	return "sudo usermod -aG kvm " + arg
}

func shellArg(s string) string {
	if s != "" && isSafeShellWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isSafeShellWord(s string) bool {
	for i := range len(s) {
		if !isSafeShellByte(s[i]) {
			return false
		}
	}
	return true
}

func isSafeShellByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_' || c == '-' || c == '.'
}
