package jail

import (
	"fmt"
	"runtime"
	"slices"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
)

// Linux's numbers. This file builds the filters on any OS, so that their
// tests run anywhere, and the OS's own constants differ.
const (
	afUnix       = 1
	afInet       = 2
	afInet6      = 10
	afNetlink    = 16
	netlinkRoute = 0

	// CLONE_NEWNS, NEWCGROUP, NEWUTS, NEWIPC, NEWUSER, NEWPID and NEWNET.
	// CLONE_NEWTIME shares its bit with clone's exit signal: only clone3
	// and unshare can ask for it, and the profile refuses both.
	cloneNewFlags = 0x00020000 | 0x02000000 | 0x04000000 | 0x08000000 | 0x10000000 | 0x20000000 | 0x40000000

	auditArchX86_64  = 0xc000003e
	auditArchAARCH64 = 0xc00000b7

	retAllow       = 0x7fff0000
	retKillProcess = 0x80000000
	retErrno       = 0x00050000
	errnoEPERM     = 1
	errnoENOSYS    = 38

	tiocsti   = 0x5412
	tioclinux = 0x541c
)

// refused are the system calls that every jail's profile refuses with
// EPERM. No jail's command needs them, and each opens kernel code, or
// another process, to the command.
var refused = []string{
	// New namespaces and mounts.
	"unshare", "setns",
	"mount", "umount2", "pivot_root", "move_mount", "open_tree",
	"fsopen", "fsmount", "fsconfig", "fspick", "mount_setattr",
	// Kernel keys, BPF, perf events, userfaultfd, io_uring, kexec and modules.
	"keyctl", "add_key", "request_key", "bpf", "perf_event_open", "userfaultfd",
	"io_uring_setup", "io_uring_enter", "io_uring_register",
	"kexec_load", "kexec_file_load", "init_module", "finit_module", "delete_module",
	// Other processes' memory. Landlock refuses these too, outside its domain.
	"ptrace", "process_vm_readv", "process_vm_writev", "pidfd_getfd",
}

// pathMeta are the system calls that change a file's mode, owner or
// extended attributes through a path, which Landlock does not check.
// Without a user namespace, where the jail sees the host's paths, every
// profile refuses them. Their forms on a descriptor (fchmod, fchown,
// fsetxattr, fremovexattr) stay allowed, because Landlock checked the open.
// utimensat is refused when it names a path, and allowed on a descriptor.
var pathMeta = []string{"fchmodat", "fchmodat2", "fchownat", "setxattr", "lsetxattr",
	"removexattr", "lremovexattr", "setxattrat", "removexattrat"}

// legacyPathMeta are the older forms that only x86_64 has.
var legacyPathMeta = []string{"chmod", "chown", "lchown", "utime", "utimes", "futimesat"}

// families returns the socket families that a profile allows, and whether
// it also allows NETLINK_ROUTE sockets. Only the egress process talks to
// the network, and Go's net package opens a NETLINK_ROUTE socket to read
// network addresses. NetNone takes AF_INET and AF_INET6 away from it too.
func families(p Profile, n NetMode) (fams []uint64, netlink bool) {
	switch {
	case p != ProfileEgress:
		return []uint64{afUnix}, false
	case n == NetNone:
		return []uint64{afUnix}, true
	default:
		return []uint64{afUnix, afInet, afInet6}, true
	}
}

// policy returns a profile's seccomp policy: it allows every system call
// but those it refuses. Without a user namespace, a jail has no PID
// namespace to end with it, so its policy also refuses setsid and setpgid:
// every process of the jail stays in the process group that ends with it.
// It then also refuses pathMeta.
//
// The socket rules compare whole 64-bit arguments, so they fail closed: a
// family with stray high bits matches no allowed family, and is refused.
func policy(p Profile, n NetMode, landlockMode bool) seccomp.Policy {
	fams, netlink := families(p, n)
	other := make(seccomp.ArgumentConditions, 0, len(fams)+1)
	for _, f := range fams {
		other = append(other, seccomp.Condition{Argument: 0, Operation: seccomp.NotEqual, Value: f})
	}
	var checks []seccomp.NameWithConditions
	if netlink {
		// Refused: a family outside the list and not netlink, or a family
		// outside the list with a protocol other than NETLINK_ROUTE.
		checks = append(checks,
			seccomp.NameWithConditions{Name: "socket", Conditions: append(slices.Clone(other),
				seccomp.Condition{Argument: 0, Operation: seccomp.NotEqual, Value: afNetlink})},
			seccomp.NameWithConditions{Name: "socket", Conditions: append(slices.Clone(other),
				seccomp.Condition{Argument: 2, Operation: seccomp.NotEqual, Value: netlinkRoute})})
	} else {
		checks = append(checks, seccomp.NameWithConditions{Name: "socket", Conditions: other})
	}
	names := slices.Clone(refused)
	if p == profileGvisorV1 && !landlockMode {
		// Only the closed, namespaced gvisor purpose can bootstrap a nested
		// runtime. The outer PID 1 has no capabilities. The command receives
		// bootstrap capabilities only in a child user namespace, so they
		// cannot reach that PID 1 or an ancestor's mounts and processes.
		names = slices.DeleteFunc(names, func(name string) bool {
			switch name {
			case "unshare", "setns", "mount", "umount2", "pivot_root", "move_mount", "open_tree", "fsopen", "fsmount", "fsconfig", "fspick", "mount_setattr",
				"ptrace", "process_vm_readv", "process_vm_writev", "pidfd_getfd", "userfaultfd":
				return true
			default:
				return false
			}
		})
	} else {
		checks = append(checks, seccomp.NameWithConditions{Name: "clone", Conditions: seccomp.ArgumentConditions{
			{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewFlags},
		}})
	}
	if landlockMode {
		names = append(names, "setsid", "setpgid")
		names = append(names, pathMeta...)
		if info, err := arch.GetInfo(""); err == nil {
			for _, n := range legacyPathMeta {
				if _, ok := info.SyscallNames[n]; ok {
					names = append(names, n)
				}
			}
		}
		checks = append(checks, seccomp.NameWithConditions{Name: "utimensat", Conditions: seccomp.ArgumentConditions{
			{Argument: 1, Operation: seccomp.NotEqual, Value: 0},
		}})
		checks = append(checks, hostProcessChecks()...)
	}
	return seccomp.Policy{
		DefaultAction: seccomp.ActionAllow,
		Syscalls: []seccomp.SyscallGroup{
			{Names: names, Action: seccomp.ActionErrno}, // EPERM
			{NamesWithCondtions: checks, Action: seccomp.ActionErrno},
			// ENOSYS, not EPERM, so that the C library falls back to
			// clone, whose flags the filter can read, as Docker does.
			{Names: []string{"clone3"}, Action: seccomp.Action(retErrno | errnoENOSYS)},
		},
	}
}

// hostProcessChecks keeps a Landlock-only jail from changing other host
// processes' resource limits, priority or scheduler state. Zero names the
// caller for these PID arguments. ioprio_set also requires WHO_PROCESS=1.
func hostProcessChecks() []seccomp.NameWithConditions {
	checks := make([]seccomp.NameWithConditions, 0, 9)
	for _, name := range []string{"prlimit64", "sched_setscheduler", "sched_setattr", "sched_setparam", "sched_setaffinity"} {
		checks = append(checks, seccomp.NameWithConditions{Name: name, Conditions: seccomp.ArgumentConditions{
			{Argument: 0, Operation: seccomp.NotEqual, Value: 0},
		}})
	}
	for _, call := range []struct {
		name string
		self uint64
	}{{"setpriority", 0}, {"ioprio_set", 1}} {
		checks = append(checks,
			seccomp.NameWithConditions{Name: call.name, Conditions: seccomp.ArgumentConditions{
				{Argument: 0, Operation: seccomp.NotEqual, Value: call.self},
			}},
			seccomp.NameWithConditions{Name: call.name, Conditions: seccomp.ArgumentConditions{
				{Argument: 1, Operation: seccomp.NotEqual, Value: 0},
			}})
	}
	return checks
}

// guard returns a second filter, written by hand. It kills the process when
// a system call comes from an architecture other than the host's own: an
// i386 call through int 0x80 on x86_64, or a 32-bit Arm call on arm64.
// go-seccomp-bpf sends such a call to the profile's default action, which
// allows it (isolation spec 4.1). The library refuses x32 calls itself.
//
// It also refuses ioctl with TIOCSTI and TIOCLINUX, which push input into a
// terminal (CVE-2017-5226). The kernel reads the request as 32 bits, so the
// filter compares the low 32 bits only: high bits do not hide it.
func guard(goarch string) ([]bpf.Instruction, error) {
	var native uint32
	switch goarch {
	case "amd64":
		native = auditArchX86_64
	case "arm64":
		native = auditArchAARCH64
	default:
		return nil, failure("jails do not support the "+goarch+" architecture",
			"Run the host helper on amd64 or arm64.")
	}
	info, err := arch.GetInfo(goarch)
	if err != nil {
		return nil, failure("no system call table for "+goarch+": "+err.Error(), "Report this as a bug.")
	}
	ioctl := uint32(info.SyscallNames["ioctl"]) //nolint:gosec // G115: system call numbers are small
	return []bpf.Instruction{
		bpf.LoadAbsolute{Off: 4, Size: 4}, // seccomp_data.arch
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: native, SkipFalse: 7},
		bpf.LoadAbsolute{Off: 0, Size: 4}, // seccomp_data.nr
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: ioctl, SkipFalse: 3},
		bpf.LoadAbsolute{Off: 24, Size: 4}, // the low half of args[1], little-endian
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: tiocsti, SkipTrue: 2},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: tioclinux, SkipTrue: 1},
		bpf.RetConstant{Val: retAllow},
		bpf.RetConstant{Val: retErrno | errnoEPERM},
		bpf.RetConstant{Val: retKillProcess},
	}, nil
}

// filters assembles a jail's two seccomp filters for this machine: the
// profile, then the guard.
func filters(p Profile, n NetMode, landlockMode bool) ([][]bpf.RawInstruction, error) {
	pol := policy(p, n, landlockMode)
	prog, err := pol.Assemble()
	if err != nil {
		return nil, failure(fmt.Sprintf("cannot build the %s seccomp profile: %v", p, err),
			"Report this as a bug: the profile names a system call that this architecture lacks.")
	}
	second, err := guard(runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	out := make([][]bpf.RawInstruction, 0, 2)
	for _, insts := range [][]bpf.Instruction{prog, second} {
		raw, err := bpf.Assemble(insts)
		if err != nil {
			return nil, failure(fmt.Sprintf("cannot assemble the %s seccomp filter: %v", p, err),
				"Report this as a bug.")
		}
		out = append(out, raw)
	}
	return out, nil
}
