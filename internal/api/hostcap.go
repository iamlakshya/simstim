package api

import (
	"strconv"
	"strings"
)

// Wall level names, as users type them and as they travel on the wire.
const (
	LevelNone    = "none"
	LevelProcess = "process"
	LevelGVisor  = "gvisor"
	LevelMicroVM = "microvm"
)

// KVM is what the sandbox probe found out about /dev/kvm (isolation spec 14.15).
type KVM struct {
	Present        bool   `json:"present"`
	Usable         bool   `json:"usable"`
	Mode           string `json:"mode,omitempty"`
	Group          string `json:"group,omitempty"`
	ACLUser        bool   `json:"acl_user,omitempty"`
	InGroup        bool   `json:"in_group,omitempty"`
	InGroupNow     bool   `json:"in_group_now,omitempty"`
	InGroupManager bool   `json:"in_group_manager,omitempty"`
}

// StaleGroup reports whether /dev/kvm is present and the user already belongs
// to its group in the host's user database, yet the probing process lacks that
// supplementary group and cannot open /dev/kvm (for example, over a persisted
// SSH ControlMaster opened before usermod -aG kvm).
func (k KVM) StaleGroup() bool {
	return k.Present && !k.Usable && k.InGroup && !k.InGroupNow
}

// Nitro is what the sandbox probe found out about /dev/nitro_enclaves.
type Nitro struct {
	Present bool             `json:"present"`
	Usable  bool             `json:"usable"`
	ACLUser bool             `json:"acl_user,omitempty"`
	Pools   map[string]int64 `json:"pools,omitempty"`
}

// CapProbe is the answer to sandbox.probe (isolation spec 14.3 and 14.15).
// It never holds a secret.
type CapProbe struct {
	OS               string      `json:"os"`
	Arch             string      `json:"arch"`
	Kernel           string      `json:"kernel"`
	Distro           string      `json:"distro"`
	User             string      `json:"user"`
	UID              int         `json:"uid"`
	Systemd          int         `json:"systemd"`
	UserManager      bool        `json:"user_manager"`
	Linger           bool        `json:"linger"`
	CgroupV2         bool        `json:"cgroup_v2"`
	Delegated        []string    `json:"delegated,omitempty"`
	KVM              KVM         `json:"kvm"`
	CPUVirt          string      `json:"cpu_virt,omitempty"`
	Virt             string      `json:"virt,omitempty"`
	Nitro            *Nitro      `json:"nitro,omitempty"`
	Bwrap            bool        `json:"bwrap"`
	BwrapPath        string      `json:"bwrap_path,omitempty"`
	BwrapVersion     string      `json:"bwrap_version,omitempty"`
	BwrapAppArmor    string      `json:"bwrap_apparmor,omitempty"`
	LandlockABI      int         `json:"landlock_abi"`
	Seccomp          bool        `json:"seccomp"`
	UserNS           bool        `json:"userns"`
	UserNSRestricted bool        `json:"apparmor_userns"`
	IOURingDisabled  int         `json:"io_uring_disabled"`
	VsockBlocked     bool        `json:"vsock_blocked"`
	Subuid           bool        `json:"subuid"`
	HasSetfacl       bool        `json:"has_setfacl"`
	HasNewuidmap     bool        `json:"has_newuidmap"`
	Sudo             bool        `json:"sudo"`
	SudoPasswordless bool        `json:"sudo_passwordless"`
	DiskFree         int64       `json:"disk_free"`
	DiskFS           string      `json:"disk_fs,omitempty"`
	Reflink          bool        `json:"reflink"`
	MemTotal         int64       `json:"mem_total"`
	MemForSessions   int64       `json:"mem_for_sessions"`
	RootSteps        []string    `json:"root_steps,omitempty"`
	Runtimes         []string    `json:"runtimes,omitempty"`
	ReleaseReachable *bool       `json:"release_reachable,omitempty"`
	Started          int         `json:"started"`
	Walls            []WallProbe `json:"walls"`
}

// RuntimeItem describes one runtime binary that a release or host provides.
type RuntimeItem struct {
	Kind       string `json:"kind"`                  // "vmm" or "gvisor"
	Name       string `json:"name"`                  // e.g. "cloud-hypervisor", "firecracker", "runsc"
	Arch       string `json:"arch,omitempty"`        // "amd64", "arm64"
	HostKernel string `json:"host_kernel,omitempty"` // e.g. ">=6.8" or "5.10,6.1,6.18"
}

// RuntimeStatus describes the wall runtimes available for a host.
type RuntimeStatus struct {
	Items []RuntimeItem `json:"items,omitempty"`
}

// LevelsFor says which wall level a host gives now, and which level it can
// give after its root or package setup step (isolation spec 14.3).
func LevelsFor(p CapProbe, r RuntimeStatus) (now, afterRoot string) {
	vmm := hasMatchingRuntime(p, r, "vmm")
	gvisor := hasMatchingRuntime(p, r, "gvisor") && kernelAtLeast(p.Kernel, 5, 6)

	now = LevelNone
	switch {
	case hasOKWall(p.Walls, LevelMicroVM) || ((p.KVM.Usable || (p.Nitro != nil && p.Nitro.Usable)) && vmm):
		now = LevelMicroVM
	case hasOKWall(p.Walls, LevelGVisor) || (p.UserNS && gvisor):
		now = LevelGVisor
	case processReadyNow(p):
		now = LevelProcess
	}

	afterRoot = now
	switch {
	case (p.KVM.Present || (p.Nitro != nil && p.Nitro.Present)) && vmm:
		afterRoot = strongerLevel(afterRoot, LevelMicroVM)
	case gvisor && (p.UserNS || p.UserNSRestricted):
		afterRoot = strongerLevel(afterRoot, LevelGVisor)
	case processReadyAfterSetup(p):
		afterRoot = strongerLevel(afterRoot, LevelProcess)
	}
	return now, afterRoot
}

func hasOKWall(walls []WallProbe, level string) bool {
	for _, w := range walls {
		if w.Level == level && w.OK {
			return true
		}
	}
	return false
}

func hasWallLevel(walls []WallProbe, level string) bool {
	for _, w := range walls {
		if w.Level == level {
			return true
		}
	}
	return false
}

func processReadyNow(p CapProbe) bool {
	if hasWallLevel(p.Walls, LevelProcess) {
		return hasOKWall(p.Walls, LevelProcess)
	}
	if p.OS != "" && p.OS != "linux" {
		return false
	}
	if p.Kernel != "" && !kernelAtLeast(p.Kernel, 5, 13) {
		return false
	}
	return p.Bwrap && p.UserNS && p.LandlockABI >= 1 && p.Seccomp
}

func processReadyAfterSetup(p CapProbe) bool {
	if processReadyNow(p) {
		return true
	}
	if p.OS != "" && p.OS != "linux" {
		return false
	}
	if p.Kernel != "" && !kernelAtLeast(p.Kernel, 5, 13) {
		return false
	}
	if p.LandlockABI < 1 || !p.Seccomp {
		return false
	}
	// Installing bubblewrap (when user namespaces already work) or loading
	// Ubuntu's bwrap-userns-restrict AppArmor profile unlocks process walls.
	return p.UserNS || p.UserNSRestricted
}

func hasMatchingRuntime(p CapProbe, r RuntimeStatus, kind string) bool {
	for _, it := range r.Items {
		if it.Kind != kind {
			continue
		}
		if it.Arch != "" && p.Arch != "" && it.Arch != p.Arch {
			continue
		}
		if it.HostKernel != "" && !kernelMatches(p.Kernel, it.HostKernel) {
			continue
		}
		return true
	}
	return false
}

func levelRank(level string) int {
	switch level {
	case LevelProcess:
		return 1
	case LevelGVisor:
		return 2
	case LevelMicroVM, "vz":
		return 3
	default:
		return 0
	}
}

func strongerLevel(a, b string) string {
	if levelRank(b) > levelRank(a) {
		return b
	}
	return a
}

// ParseKernelMajorMinor extracts the leading major and minor version numbers
// from a Linux kernel release string such as "6.12.118+deb13-cloud-amd64".
func ParseKernelMajorMinor(release string) (major, minor int, ok bool) {
	release = strings.TrimSpace(release)
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	maj, ok := parseLeadingInt(parts[0])
	if !ok {
		return 0, 0, false
	}
	minVal, ok := parseLeadingInt(parts[1])
	if !ok {
		return 0, 0, false
	}
	return maj, minVal, true
}

func parseLeadingInt(s string) (int, bool) {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	v, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0, false
	}
	return v, true
}

func kernelAtLeast(release string, wantMajor, wantMinor int) bool {
	maj, minVal, ok := ParseKernelMajorMinor(release)
	if !ok {
		return false
	}
	return maj > wantMajor || (maj == wantMajor && minVal >= wantMinor)
}

func kernelMatches(release, constraint string) bool {
	maj, minVal, ok := ParseKernelMajorMinor(release)
	if !ok {
		return false
	}
	for _, part := range strings.Split(constraint, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if rest, ge := strings.CutPrefix(part, ">="); ge {
			wmaj, wmin, wok := ParseKernelMajorMinor(strings.TrimSpace(rest))
			if wok && (maj > wmaj || (maj == wmaj && minVal >= wmin)) {
				return true
			}
			continue
		}
		wmaj, wmin, wok := ParseKernelMajorMinor(part)
		if wok && maj == wmaj && minVal == wmin {
			return true
		}
	}
	return false
}
