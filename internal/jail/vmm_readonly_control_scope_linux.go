//go:build linux

package jail

import (
	"runtime"
	"unsafe"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// The already-running B controls only its original VM directory and native
// inherited streams. It has no KVM, executable or other session path grant.
func enforceReadonlyControlV1(c *setup) error {
	if c == nil || c.ReadonlyVMMInitV1 == nil || len(c.Binds) == 0 {
		return vmmModeFailureV1("the original control scope lost its VM directory")
	}
	abi := kernelABI()
	if abi < 1 {
		return vmmModeFailureV1("the original read-only control requires actual Landlock")
	}
	rs, err := newRuleset(abi, NetNone)
	if err != nil {
		return err
	}
	if err := rs.add(c.Binds[0].FD, Read|Write|Connect); err != nil {
		return err
	}
	if err := rs.addPath("/proc/thread-self", Read); err != nil {
		return err
	}
	if err := rs.addPath("/proc/self/fdinfo", Read); err != nil {
		return err
	}
	if err := rs.add(c.VMMExecV1.LeaseFD, Read); err != nil {
		return err
	}
	if err := rs.enforce(); err != nil {
		return err
	}
	for _, bound := range c.Binds {
		if err := closeVMMCapturedPinV1(bound.FD, bound.Dev, bound.Ino); err != nil {
			return err
		}
	}
	for _, rule := range pinnedRules(c.Rules) {
		if err := closeVMMCapturedPinV1(rule.FD, rule.Dev, rule.Ino); err != nil {
			return err
		}
	}
	instructions, err := readonlyControlFilterV1(runtime.GOARCH)
	if err != nil {
		return err
	}
	raw, err := bpf.Assemble(instructions)
	if err != nil {
		return err
	}
	program := make([]unix.SockFilter, len(raw))
	for index, one := range raw {
		program[index] = unix.SockFilter{Code: one.Op, Jt: one.Jt, Jf: one.Jf, K: one.K}
	}
	filter := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	//nolint:gosec // The native syscall consumes the bounded fixed BPF programme.
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&filter)))
	runtime.KeepAlive(program)
	if errno != 0 {
		return errno
	}
	if result != 0 {
		return vmmModeFailureV1("the original control scope did not restrict every native thread")
	}
	return verifyReadonlyCollapsedV1(c)
}

func readonlyControlFilterV1(goarch string) ([]bpf.Instruction, error) {
	info, err := arch.GetInfo(goarch)
	if err != nil {
		return nil, err
	}
	out := []bpf.Instruction{}
	deny := bpf.RetConstant{Val: retErrno | errnoEPERM}
	for _, name := range []string{"execve", "execveat"} {
		number, ok := info.SyscallNames[name]
		if !ok {
			return nil, vmmModeFailureV1("the original control architecture lacks its fixed exec guard")
		}
		out = append(out, bpf.LoadAbsolute{Off: 0, Size: 4}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(number), SkipFalse: 1}, deny)
	}
	number, ok := info.SyscallNames["prctl"]
	if !ok {
		return nil, vmmModeFailureV1("the original control architecture lacks prctl")
	}
	out = append(out, bpf.LoadAbsolute{Off: 0, Size: 4}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(number), SkipFalse: 5}, bpf.LoadAbsolute{Off: 16, Size: 4}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.PR_SET_DUMPABLE, SkipFalse: 3}, bpf.LoadAbsolute{Off: 24, Size: 4}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: 0, SkipTrue: 1}, deny, bpf.RetConstant{Val: retAllow})
	return out, nil
}
