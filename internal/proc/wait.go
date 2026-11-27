//go:build linux || darwin

// Package proc waits for a child process's exit without reaping it. Until
// its parent reaps it, a child's PID, and the process group that it leads,
// name nothing else: the parent may still signal them. Every process that
// signals a group or a PID follows that rule (docs/explanation/architecture.md,
// 2.5).
package proc

import "syscall"

// The codes of siginfo_t for a child that exited, and for one that a
// signal ended (asm-generic/siginfo.h, sys/signal.h).
const (
	cldKilled = 2
	cldDumped = 3
)

// waitStatus is the wait status of a child from the code and the status of
// its siginfo_t: the exit code for an exit, the signal for a kill.
func waitStatus(code, status int32) syscall.WaitStatus {
	switch code {
	case cldKilled:
		return syscall.WaitStatus(status & 0x7f)
	case cldDumped:
		return syscall.WaitStatus(status&0x7f | 0x80) //nolint:gosec // G115: masked to 8 bits
	}
	return syscall.WaitStatus((status & 0xff) << 8)
}
