//go:build linux

package jail

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	seccomp "github.com/elastic/go-seccomp-bpf"
	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

const (
	// seccompArchOffset is offsetof(struct seccomp_data, arch).
	seccompArchOffset = 4

	// cloneNewMask matches every CLONE_NEW* namespace creation flag on clone(2).
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

// SeccompPolicy returns the seccomp policy for a jailed trusted-side command:
// default allow, refusing ptrace/process_vm_*, namespace creation, mount
// syscalls, dangerous kernel surfaces, and (for NetNone) all non-AF_UNIX
// sockets including AF_INET and AF_INET6. clone3 returns ENOSYS so runtimes
// fall back to clone(2) where CLONE_NEW* flags are filtered.
func SeccompPolicy(_ Profile, netMode NetMode) seccomp.Policy {
	conds := []seccomp.NameWithConditions{
		{
			// Block clone(2) with any CLONE_NEW* flag.
			Name: "clone",
			Conditions: seccomp.ArgumentConditions{
				{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewMask},
			},
		},
	}

	if netMode == NetNone {
		// Refuse every non-AF_UNIX socket family (blocks AF_INET, AF_INET6,
		// AF_VSOCK, AF_PACKET, AF_NETLINK, etc.; isolation spec 5.6).
		conds = append(conds, seccomp.NameWithConditions{
			Name: "socket",
			Conditions: seccomp.ArgumentConditions{
				{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_UNIX},
			},
		})
	} else {
		// NetHost (e.g. egress): allow AF_UNIX, AF_INET, AF_INET6 and
		// AF_NETLINK(NETLINK_ROUTE); block AF_VSOCK, AF_PACKET and other
		// netlink protocols.
		conds = append(conds,
			seccomp.NameWithConditions{
				Name: "socket",
				Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_UNIX},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET6},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_NETLINK},
				},
			},
			seccomp.NameWithConditions{
				Name: "socket",
				Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.Equal, Value: unix.AF_NETLINK},
					{Argument: 2, Operation: seccomp.NotEqual, Value: unix.NETLINK_ROUTE},
				},
			},
		)
	}

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
					// Cross-process tracing and memory access.
					"ptrace",
					"process_vm_readv",
					"process_vm_writev",
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
				NamesWithCondtions: conds,
			},
		},
	}
}

// SeccompInstructions compiles the seccomp policy for profile and netMode with
// a strict host-architecture guard that kills foreign ABIs (such as i386
// int 0x80 on amd64 or 32-bit ARM on arm64).
func SeccompInstructions(profile Profile, netMode NetMode) ([]bpf.RawInstruction, error) {
	if !validProfile(profile) {
		return nil, fmt.Errorf("jail: unknown seccomp profile %q. Try: use vmm, converter, git or egress", profile)
	}
	info, err := arch.GetInfo("")
	if err != nil {
		return nil, fmt.Errorf("jail seccomp: detect host architecture: %w. Try: run on Linux amd64 or arm64", err)
	}
	pol := SeccompPolicy(profile, netMode)
	policyInsts, err := pol.Assemble()
	if err != nil {
		return nil, fmt.Errorf("jail seccomp: assemble policy: %w. Try: check the host architecture and seccomp rules", err)
	}

	insts := make([]bpf.Instruction, 0, 3+len(policyInsts))
	insts = append(insts,
		bpf.LoadAbsolute{Off: seccompArchOffset, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(info.ID), SkipTrue: 1},
		bpf.RetConstant{Val: uint32(seccomp.ActionKillProcess)},
	)
	insts = append(insts, policyInsts...)

	raw, err := bpf.Assemble(insts)
	if err != nil {
		return nil, fmt.Errorf("jail seccomp: assemble BPF: %w. Try: report this bug with the host architecture", err)
	}
	return raw, nil
}

// SeccompBPF returns native-endian Linux struct sock_filter bytes ready for
// bubblewrap's --seccomp <fd> flag.
func SeccompBPF(profile Profile, netMode NetMode) ([]byte, error) {
	raw, err := SeccompInstructions(profile, netMode)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(len(raw) * 8)
	for _, ins := range raw {
		_ = binary.Write(&buf, binary.NativeEndian, ins.Op)
		buf.WriteByte(ins.Jt)
		buf.WriteByte(ins.Jf)
		_ = binary.Write(&buf, binary.NativeEndian, ins.K)
	}
	return buf.Bytes(), nil
}

// installSeccompBPF installs raw into the current OS thread (synchronizing
// across threads via SECCOMP_FILTER_FLAG_TSYNC when supported) after setting
// PR_SET_NO_NEW_PRIVS.
func installSeccompBPF(raw []bpf.RawInstruction) error {
	if len(raw) == 0 || len(raw) > 0xffff {
		return errors.New("jail seccomp: invalid BPF instruction count")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("jail seccomp: set PR_SET_NO_NEW_PRIVS: %w", err)
	}
	filters := make([]unix.SockFilter, len(raw))
	for i, ins := range raw {
		filters[i] = unix.SockFilter{
			Code: ins.Op,
			Jt:   ins.Jt,
			Jf:   ins.Jf,
			K:    ins.K,
		}
	}
	prog := unix.SockFprog{
		//nolint:gosec // G115: len(filters) is checked against 0xffff above.
		Len:    uint16(len(filters)),
		Filter: &filters[0],
	}
	//nolint:gosec // G103: seccomp(2) expects a pointer to struct sock_fprog.
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if errno == 0 {
		return nil
	}
	if errno == unix.ENOSYS || errno == unix.EINVAL {
		//nolint:gosec // G103: PR_SET_SECCOMP expects a pointer to struct sock_fprog.
		if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
			return fmt.Errorf("jail seccomp: install filter via prctl: %w", err)
		}
		return nil
	}
	return fmt.Errorf("jail seccomp: install filter: %w", errno)
}
