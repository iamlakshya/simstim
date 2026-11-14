package api

import (
	"strconv"
	"strings"
)

// KVM is what the probe found about /dev/kvm.
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

// Nitro is what the probe found about /dev/nitro_enclaves.
type Nitro struct {
	Present bool             `json:"present"`
	Usable  bool             `json:"usable"`
	ACLUser bool             `json:"acl_user,omitempty"`
	Pools   map[string]int64 `json:"pools,omitempty"`
}

// CapProbe is the answer to sandbox.probe. It never holds a secret.
type CapProbe struct {
	OS                string      `json:"os"`
	Arch              string      `json:"arch"`
	Kernel            string      `json:"kernel"`
	Distro            string      `json:"distro"`
	User              string      `json:"user"`
	UID               int         `json:"uid"`
	Systemd           int         `json:"systemd"`
	UserManager       bool        `json:"user_manager"`
	Linger            bool        `json:"linger"`
	CgroupV2          bool        `json:"cgroup_v2"`
	Delegated         []string    `json:"delegated"`
	KVM               KVM         `json:"kvm"`
	CPUVirt           string      `json:"cpu_virt"`
	Virt              string      `json:"virt"`
	Nitro             *Nitro      `json:"nitro,omitempty"`
	LandlockABI       int         `json:"landlock_abi"`
	Seccomp           bool        `json:"seccomp"`
	UserNS            bool        `json:"userns"`
	UserNSRestricted  bool        `json:"apparmor_userns"`
	UserNSCopyCurrent bool        `json:"userns_copy_current"`
	IOURingDisabled   int         `json:"io_uring_disabled"`
	VsockBlocked      bool        `json:"vsock_blocked"`
	Subuid            bool        `json:"subuid"`
	HasSetfacl        bool        `json:"has_setfacl"`
	HasNewuidmap      bool        `json:"has_newuidmap"`
	Sudo              bool        `json:"sudo"`
	SudoPasswordless  bool        `json:"sudo_passwordless"`
	DiskFree          int64       `json:"disk_free"`
	DiskFS            string      `json:"disk_fs"`
	Reflink           bool        `json:"reflink"`
	MemTotal          int64       `json:"mem_total"`
	MemForSessions    int64       `json:"mem_for_sessions"`
	RootSteps         []string    `json:"root_steps,omitempty"`
	Runtimes          []string    `json:"runtimes,omitempty"`
	ReleaseReachable  *bool       `json:"release_reachable,omitempty"`
	Started           int         `json:"started"`
	Walls             []WallProbe `json:"walls"`
}

// RuntimeDriver describes a complete driver and its verified installed runtime.
// KernelMin is a major.minor minimum. KernelSeries limits support to the named
// major.minor series when the runtime tests only those host kernels.
type RuntimeDriver struct {
	Name         string   `json:"name"`
	Arch         string   `json:"arch"`
	KernelMin    string   `json:"kernel_min,omitempty"`
	KernelSeries []string `json:"kernel_series,omitempty"`
	Verified     bool     `json:"verified"`
}

// RuntimeStatus carries file and transfer claims and the separate driver
// compatibility input for LevelsFor. A downloaded file alone never verifies
// a driver. Only a complete registered driver can provide that input.
type RuntimeStatus struct {
	Drivers   []RuntimeDriver   `json:"drivers"`
	Runtime   string            `json:"runtime,omitempty"`
	Items     []RuntimeFile     `json:"items"`
	Transfers []RuntimeTransfer `json:"transfers"`
}

// LevelsFor says which level a host gives now and after the root step. A root
// step can remove an access restriction. It cannot install a missing driver,
// provide a runtime, or add support for a different kernel or architecture.
func LevelsFor(p CapProbe, r RuntimeStatus) (now, afterRoot string) {
	nowRank, afterRank := 0, 0
	for _, probe := range p.Walls {
		rank := levelRank(probe.Level)
		if rank == 0 || !levelPrerequisites(p, r, probe) {
			continue
		}
		if probe.OK && driverReadyNow(p, probe) {
			nowRank = max(nowRank, rank)
			afterRank = max(afterRank, rank)
			continue
		}
		if rootCanFix(p, probe) {
			afterRank = max(afterRank, rank)
		}
	}
	return levelName(nowRank), levelName(afterRank)
}

func driverReadyNow(p CapProbe, probe WallProbe) bool {
	switch probe.Level {
	case "process", "gvisor":
		return p.UserNS
	case "microvm":
		if probe.Driver == "nitro" {
			return p.Nitro != nil && p.Nitro.Usable
		}
		return p.KVM.Usable
	default:
		return false
	}
}

func levelPrerequisites(p CapProbe, r RuntimeStatus, probe WallProbe) bool {
	if p.OS != "linux" || probe.Facts["runtime_available"] == "false" {
		return false
	}
	switch probe.Level {
	case "process":
		return probe.Driver == "process" && p.LandlockABI >= 1 && p.Seccomp && kernelAtLeast(p.Kernel, "5.13")
	case "gvisor":
		return probe.Driver == "gvisor" && r.supports(probe.Driver, p) && kernelAtLeast(p.Kernel, "5.6")
	case "microvm":
		return r.supports(probe.Driver, p) && microVMKernel(probe.Driver, p.Kernel)
	default:
		return false
	}
}

func rootCanFix(p CapProbe, probe WallProbe) bool {
	// A driver's named blocker certifies that all other prerequisite checks
	// passed. A generic failed probe must never turn into a promise after root.
	switch probe.Facts["root_blocker"] {
	case "kvm_access":
		return probe.Level == "microvm" && probe.Driver != "nitro" && p.KVM.Present && !p.KVM.Usable
	case "nitro_access":
		return probe.Level == "microvm" && probe.Driver == "nitro" && p.Nitro != nil && p.Nitro.Present && !p.Nitro.Usable
	case "apparmor_userns":
		return (probe.Level == "process" || probe.Level == "gvisor") && p.UserNSRestricted
	default:
		return false
	}
}

func (r RuntimeStatus) supports(name string, p CapProbe) bool {
	for _, d := range r.Drivers {
		if d.Name != name || !d.Verified || d.Arch != p.Arch || !kernelAtLeast(p.Kernel, d.KernelMin) {
			continue
		}
		if len(d.KernelSeries) == 0 {
			return true
		}
		major, minor, ok := kernelParts(p.Kernel)
		if !ok {
			continue
		}
		for _, series := range d.KernelSeries {
			sMajor, sMinor, valid := kernelParts(series)
			if valid && major == sMajor && minor == sMinor {
				return true
			}
		}
	}
	return false
}

func microVMKernel(driver, kernel string) bool {
	switch driver {
	case "chv":
		return kernelAtLeast(kernel, "6.8")
	case "firecracker":
		major, minor, ok := kernelParts(kernel)
		return ok && ((major == 5 && minor == 10) || (major == 6 && (minor == 1 || minor == 18)))
	case "nitro":
		// Nitro support may be backported. Its driver probe checks the sysfs
		// interface and its runtime record carries any kernel restrictions.
		_, _, ok := kernelParts(kernel)
		return ok
	default:
		return false
	}
}

func kernelAtLeast(kernel, minimum string) bool {
	major, minor, ok := kernelParts(kernel)
	if !ok {
		return false
	}
	if minimum == "" {
		return true
	}
	mMajor, mMinor, valid := kernelParts(minimum)
	return valid && (major > mMajor || (major == mMajor && minor >= mMinor))
}

func kernelParts(kernel string) (major, minor int, ok bool) {
	parts := strings.SplitN(kernel, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	var err error
	if major, err = strconv.Atoi(parts[0]); err != nil || major < 0 {
		return 0, 0, false
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil || minor < 0 {
		return 0, 0, false
	}
	return major, minor, true
}

func levelRank(level string) int {
	switch level {
	case "process":
		return 1
	case "gvisor":
		return 2
	case "microvm":
		return 3
	default:
		return 0
	}
}

func levelName(rank int) string {
	switch rank {
	case 1:
		return "process"
	case 2:
		return "gvisor"
	case 3:
		return "microvm"
	default:
		return "none"
	}
}
