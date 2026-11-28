//go:build linux

package process

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"syscall"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const (
	// seccompArchOffset is offsetof(struct seccomp_data, arch).
	seccompArchOffset = 4

	// cloneNewMask matches every CLONE_NEW* namespace creation flag passed to clone(2).
	cloneNewMask uint64 = unix.CLONE_NEWNS |
		unix.CLONE_NEWUTS |
		unix.CLONE_NEWIPC |
		unix.CLONE_NEWUSER |
		unix.CLONE_NEWPID |
		unix.CLONE_NEWNET |
		unix.CLONE_NEWCGROUP |
		unix.CLONE_NEWTIME

	actionEPERM  = seccomp.ActionErrno
	actionENOSYS = seccomp.ActionErrno | seccomp.Action(syscall.ENOSYS)
)

// SeccompPolicy returns the process-wall seccomp policy of isolation spec 4.1:
// default allow, denying namespace creation, mount syscalls, kernel-surface
// syscalls, non-allowlisted socket families and non-route netlink protocols,
// and returning ENOSYS for clone3 so libc falls back to clone.
func SeccompPolicy() seccomp.Policy {
	return seccomp.Policy{
		DefaultAction: seccomp.ActionAllow,
		Syscalls: []seccomp.SyscallGroup{
			{
				Action: actionENOSYS,
				Names:  []string{"clone3"},
			},
			{
				Action: actionEPERM,
				Names: []string{
					// Namespace creation and switching.
					"unshare",
					"setns",
					// Mount family.
					"mount",
					"umount2",
					"pivot_root",
					"move_mount",
					"open_tree",
					"fsopen",
					"fsmount",
					"fsconfig",
					"fspick",
					"mount_setattr",
					// Dangerous kernel surfaces.
					"keyctl",
					"add_key",
					"request_key",
					"bpf",
					"perf_event_open",
					"userfaultfd",
					"io_uring_setup",
					"io_uring_enter",
					"io_uring_register",
					"kexec_load",
					"kexec_file_load",
					"init_module",
					"finit_module",
					"delete_module",
				},
				NamesWithCondtions: []seccomp.NameWithConditions{
					{
						// Block clone(2) with any CLONE_NEW* flag.
						Name: "clone",
						Conditions: seccomp.ArgumentConditions{
							{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewMask},
						},
					},
					{
						// Allow only AF_UNIX, AF_INET, AF_INET6 and AF_NETLINK sockets
						// (blocks AF_VSOCK, AF_PACKET, etc.).
						Name: "socket",
						Conditions: seccomp.ArgumentConditions{
							{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_UNIX},
							{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET},
							{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET6},
							{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_NETLINK},
						},
					},
					{
						// Allow only NETLINK_ROUTE (0) on AF_NETLINK so glibc getifaddrs
						// and Node os.networkInterfaces() work, while blocking NETLINK_AUDIT,
						// NETLINK_NETFILTER, NETLINK_KOBJECT_UEVENT and NETLINK_SOCK_DIAG.
						Name: "socket",
						Conditions: seccomp.ArgumentConditions{
							{Argument: 0, Operation: seccomp.Equal, Value: unix.AF_NETLINK},
							{Argument: 2, Operation: seccomp.NotEqual, Value: unix.NETLINK_ROUTE},
						},
					},
				},
			},
		},
	}
}

// SeccompBPF compiles SeccompPolicy() with a strict host-architecture guard
// (killing foreign ABIs such as i386 int 0x80 on x86_64 or 32-bit ARM on
// aarch64) into native-endian struct sock_filter bytes ready for bwrap
// --seccomp <fd>.
func SeccompBPF() ([]byte, error) {
	info, err := arch.GetInfo("")
	if err != nil {
		return nil, fmt.Errorf("seccomp: detect host architecture: %w. Try: run on Linux amd64 or arm64", err)
	}
	pol := SeccompPolicy()
	policyInsts, err := pol.Assemble()
	if err != nil {
		return nil, fmt.Errorf("seccomp: assemble policy: %w. Try: check the host architecture and seccomp rules", err)
	}

	// go-seccomp-bpf sends a foreign arch to DefaultAction (Allow). Prepend a
	// 3-instruction host-ABI guard that kills any foreign ABI immediately
	// (spec 4.1: blocks i386 int 0x80 and 32-bit ARM). Every jump in
	// policyInsts is relative, so prepending keeps all offsets valid.
	insts := make([]bpf.Instruction, 0, 3+len(policyInsts))
	insts = append(insts,
		bpf.LoadAbsolute{Off: seccompArchOffset, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(info.ID), SkipTrue: 1},
		bpf.RetConstant{Val: uint32(seccomp.ActionKillProcess)},
	)
	insts = append(insts, policyInsts...)

	raw, err := bpf.Assemble(insts)
	if err != nil {
		return nil, fmt.Errorf("seccomp: assemble BPF: %w. Try: report this bug with the host architecture", err)
	}
	return encodeSockFilters(raw), nil
}

// encodeSockFilters serialises raw BPF instructions as native-endian Linux
// struct sock_filter records (8 bytes each: u16 code, u8 jt, u8 jf, u32 k).
func encodeSockFilters(raw []bpf.RawInstruction) []byte {
	var buf bytes.Buffer
	buf.Grow(len(raw) * 8)
	for _, ins := range raw {
		_ = binary.Write(&buf, binary.NativeEndian, ins.Op)
		buf.WriteByte(ins.Jt)
		buf.WriteByte(ins.Jf)
		_ = binary.Write(&buf, binary.NativeEndian, ins.K)
	}
	return buf.Bytes()
}
