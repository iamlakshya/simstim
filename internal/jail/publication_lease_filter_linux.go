//go:build linux

package jail

import (
	"runtime"
	"unsafe"

	"github.com/elastic/go-seccomp-bpf/arch"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// Generic policy is unchanged. The fixed pure-Go parser cannot drop or
// replace its private fence while it can still write, or unlock the shared
// OFD through any alias. Only irreversible process exit releases its raw slot.
// Integer FD/command arguments compare their low 32 bits, as the kernel does.
func protectPublicationFenceLocksV1(fd int) error {
	return protectPublicationFenceV1(fd, false)
}
func protectPublicationFenceV1(fd int, worker bool) error {
	instructions, err := publicationFencePhaseFilterV1(runtime.GOARCH, fd, worker)
	if err != nil {
		return err
	}
	raw, err := bpf.Assemble(instructions)
	if err != nil {
		return err
	}
	if !worker {
		return loadFilter(raw)
	}
	// The Go converter runtime already has multiple threads after exec.
	// Apply the entry restriction to all of them before parsing any input.
	program := make([]unix.SockFilter, len(raw))
	for i, r := range raw {
		program[i] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	filter := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]} //nolint:gosec // the fixed program is bounded below 65536 instructions
	//nolint:gosec // seccomp(2) reads the fixed native BPF program.
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&filter)))
	runtime.KeepAlive(program)
	if errno != 0 {
		return errno
	}
	if result != 0 {
		return publicationLeaseFailureV1("the converter fence could not restrict every original thread")
	}
	return nil
}
func publicationFenceFilterV1(goarch string, fd int) ([]bpf.Instruction, error) {
	return publicationFencePhaseFilterV1(goarch, fd, true)
}
func publicationFencePhaseFilterV1(goarch string, fd int, worker bool) ([]bpf.Instruction, error) {
	if fd < 3 || fd > 1<<31-1 {
		return nil, publicationLeaseFailureV1("the final publication slot is invalid")
	}
	info, err := arch.GetInfo(goarch)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"close", "close_range", "dup3", "fcntl", "flock", "execve", "execveat", "io_setup", "io_submit"} {
		if _, ok := info.SyscallNames[name]; !ok {
			return nil, publicationLeaseFailureV1("the architecture lacks a required publication fence syscall guard")
		}
	}
	var out []bpf.Instruction
	deny := bpf.RetConstant{Val: retErrno | errnoEPERM}
	block := func(name string, predicate []bpf.Instruction) {
		number, ok := info.SyscallNames[name]
		if !ok {
			return
		}
		out = append(out, bpf.LoadAbsolute{Off: 0, Size: 4}, bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(number), SkipFalse: uint8(len(predicate))})
		out = append(out, predicate...)
	}
	// No converter opens or executes another program or uses asynchronous AIO.
	// Namespace/path metadata could mutate a passed original sidecar despite
	// its readonly FD, so these unnecessary operations are also refused here.
	for _, name := range []string{"flock", "io_setup", "io_submit", "io_uring_setup", "io_uring_enter", "io_uring_register", "chmod", "fchmodat", "fchmodat2", "chown", "lchown", "fchownat", "setxattr", "lsetxattr", "removexattr", "lremovexattr", "setxattrat", "removexattrat"} {
		block(name, []bpf.Instruction{deny})
	}
	// The first filter must permit its own fixed converter exec. The closed
	// converter entry adds the second layer before reading any untrusted input.
	if worker {
		for _, name := range []string{"execve", "execveat"} {
			block(name, []bpf.Instruction{deny})
		}
	}
	oneFD := func(argument uint32) []bpf.Instruction {
		return []bpf.Instruction{
			bpf.LoadAbsolute{Off: 16 + 8*argument, Size: 4},
			bpf.JumpIf{Cond: bpf.JumpEqual, Val: uint32(fd), SkipFalse: 1}, deny,
		}
	}
	for _, name := range []string{"close", "fchmod", "fchown", "fsetxattr", "fremovexattr"} {
		block(name, oneFD(0))
	}
	for _, name := range []string{"dup2", "dup3"} {
		block(name, oneFD(1))
	}
	block("close_range", []bpf.Instruction{
		bpf.LoadAbsolute{Off: 16, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpGreaterThan, Val: uint32(fd), SkipTrue: 3},
		bpf.LoadAbsolute{Off: 24, Size: 4},
		bpf.JumpIf{Cond: bpf.JumpGreaterOrEqual, Val: uint32(fd), SkipFalse: 1}, deny,
	})
	commands := []uint32{unix.F_SETLK, unix.F_SETLKW, unix.F_OFD_SETLK, unix.F_OFD_SETLKW, unix.F_SETLEASE, unix.F_NOTIFY}
	fcntl := []bpf.Instruction{bpf.LoadAbsolute{Off: 24, Size: 4}}
	for _, command := range commands {
		fcntl = append(fcntl, bpf.JumpIf{Cond: bpf.JumpEqual, Val: command, SkipFalse: 1}, deny)
	}
	fcntl = append(fcntl, bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.F_SETFD, SkipFalse: 3})
	fcntl = append(fcntl, oneFD(0)...)
	block("fcntl", fcntl)
	block("fcntl64", fcntl)
	out = append(out, bpf.RetConstant{Val: retAllow})
	return out, nil
}
