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

	// cloneNewMask matches every CLONE_NEW* namespace creation flag valid on
	// clone(2). CLONE_NEWTIME (0x80) is omitted because bits 0..7 are CSIGNAL
	// on clone(2); unshare(2) and clone3(2) are blocked unconditionally.
	cloneNewMask uint64 = unix.CLONE_NEWNS |
		unix.CLONE_NEWUTS |
		unix.CLONE_NEWIPC |
		unix.CLONE_NEWUSER |
		unix.CLONE_NEWPID |
		unix.CLONE_NEWNET |
		unix.CLONE_NEWCGROUP

	actionEPERM  = seccomp.ActionErrno | seccomp.Action(syscall.EPERM)
	actionENOSYS = seccomp.ActionErrno | seccomp.Action(syscall.ENOSYS)
)

// SeccompPolicy returns the seccomp policy for a jailed trusted-side command:
// default allow, refusing ptrace/process_vm_*, namespace creation, mount
// syscalls, dangerous kernel surfaces, and (for NetNone) all non-AF_UNIX
// sockets including AF_INET and AF_INET6. clone3 returns ENOSYS so runtimes
// fall back to clone(2) where CLONE_NEW* flags are filtered.
func SeccompPolicy(_ Profile, netMode NetMode) seccomp.Policy {
	var socketConds []seccomp.NameWithConditions
	if netMode == NetNone {
		// Refuse every non-AF_UNIX socket family (blocks AF_INET, AF_INET6,
		// AF_VSOCK, AF_PACKET, AF_NETLINK, etc.; isolation spec 5.6).
		socketConds = []seccomp.NameWithConditions{
			{
				Name: "socket",
				Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_UNIX},
				},
			},
		}
	} else {
		// NetHost (e.g. egress): allow AF_UNIX, AF_INET, AF_INET6 and
		// AF_NETLINK(NETLINK_ROUTE); block AF_VSOCK, AF_PACKET and other
		// netlink protocols.
		socketConds = []seccomp.NameWithConditions{
			{
				Name: "socket",
				Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_UNIX},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_INET6},
					{Argument: 0, Operation: seccomp.NotEqual, Value: unix.AF_NETLINK},
				},
			},
			{
				Name: "socket",
				Conditions: seccomp.ArgumentConditions{
					{Argument: 0, Operation: seccomp.Equal, Value: unix.AF_NETLINK},
					{Argument: 2, Operation: seccomp.NotEqual, Value: unix.NETLINK_ROUTE},
				},
			},
		}
	}

	// Place each conditional syscall in its own SyscallGroup so
	// SeccompInstructions can reload seccomp_data.nr before each group;
	// go-seccomp-bpf v1.6.0 does not reload the BPF accumulator after
	// evaluating argument conditions on an earlier syscall.
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
			},
			{
				Action: actionEPERM,
				NamesWithCondtions: []seccomp.NameWithConditions{
					{
						// Block clone(2) with any CLONE_NEW* flag.
						Name: "clone",
						Conditions: seccomp.ArgumentConditions{
							{Argument: 0, Operation: seccomp.BitsSet, Value: cloneNewMask},
						},
					},
				},
			},
			{
				Action:             actionEPERM,
				NamesWithCondtions: socketConds,
			},
		},
	}
}

// SeccompInstructions compiles the seccomp policy for profile and netMode with
// a strict host-architecture guard that kills foreign ABIs (such as i386
// int 0x80 on amd64 or 32-bit ARM on arm64). Each SyscallGroup is assembled
// separately so seccomp_data.nr is reloaded before every conditional syscall
// check, preventing BPF accumulator clobbering across conditional rules.
func SeccompInstructions(profile Profile, netMode NetMode) ([]bpf.RawInstruction, error) {
	if !validProfile(profile) {
		return nil, fmt.Errorf("jail: unknown seccomp profile %q. Try: use vmm, converter, git or egress", profile)
	}
	info, err := arch.GetInfo("")
	if err != nil {
		return nil, fmt.Errorf("jail seccomp: detect host architecture: %w. Try: run on Linux amd64 or arm64", err)
	}
	pol := SeccompPolicy(profile, netMode)

	insts := []bpf.Instruction{
		bpf.LoadAbsolute{Off: seccompArchOffset, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(info.ID), SkipTrue: 1},
		bpf.RetConstant{Val: uint32(seccomp.ActionKillProcess)},
	}
	// Assemble each unconditional Name list and each individual conditional
	// NameWithConditions rule as its own stage. go-seccomp-bpf v1.6.0 does not
	// reload seccomp_data.nr into BPF accumulator A after evaluating argument
	// conditions, so combining two conditional rules in one Assemble call
	// clobbers A for the second rule.
	var stages []seccomp.SyscallGroup
	for _, grp := range pol.Syscalls {
		if len(grp.Names) > 0 {
			stages = append(stages, seccomp.SyscallGroup{
				Action: grp.Action,
				Names:  grp.Names,
			})
		}
		for _, nwc := range grp.NamesWithCondtions {
			stages = append(stages, seccomp.SyscallGroup{
				Action:             grp.Action,
				NamesWithCondtions: []seccomp.NameWithConditions{nwc},
			})
		}
	}

	wantAllow := bpf.RetConstant{Val: uint32(seccomp.ActionAllow)}
	for _, grp := range stages {
		stage := seccomp.Policy{
			DefaultAction: seccomp.ActionAllow,
			Syscalls:      []seccomp.SyscallGroup{grp},
		}
		stageInsts, serr := stage.Assemble()
		if serr != nil {
			return nil, fmt.Errorf("jail seccomp: assemble policy: %w. Try: check the host architecture and seccomp rules", serr)
		}
		if len(stageInsts) == 0 || stageInsts[len(stageInsts)-1] != wantAllow {
			return nil, errors.New("jail seccomp: unexpected assembled policy terminator. Try: check go-seccomp-bpf version")
		}
		// Strip the stage's trailing RET ALLOW so fall-through and non-match
		// jumps land directly on the next stage, which reloads seccomp_data.nr.
		insts = append(insts, stageInsts[:len(stageInsts)-1]...)
	}
	insts = append(insts, wantAllow)

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
