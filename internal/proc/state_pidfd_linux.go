//go:build linux

package proc

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ChildPidfdStateV2 is a current kernel observation, never process ownership,
// reversible preparation, a stopped Source or permission to signal a group.
type ChildPidfdStateV2 uint8

const (
	ChildNoEventV2 ChildPidfdStateV2 = iota
	ChildStoppedV2
	ChildContinuedV2
	ChildExitedV2
)

// ObserveChildPidfdV2 reads an actual child through its already-owned original
// creation pidfd. It never looks up a PID, consumes a wait event or reaps it.
// ECHILD remains unknown: a reaped or non-child handle cannot become a pause
// or exit Source here. The caller separately owns creation/restoration/Wait.
func ObserveChildPidfdV2(fd int) (ChildPidfdStateV2, error) {
	if fd < 0 {
		return ChildNoEventV2, unix.EBADF
	}
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PIDFD, fd, &info, unix.WEXITED|unix.WSTOPPED|unix.WCONTINUED|unix.WNOWAIT|unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return ChildNoEventV2, err
		}
		child := (*chldInfo)(unsafe.Pointer(&info)) //nolint:gosec // G103: the existing native 128-byte SIGCHLD layout, without reaping
		if child.Signo == 0 && child.Code == 0 && child.Pid == 0 {
			return ChildNoEventV2, nil
		}
		if child.Signo != int32(syscall.SIGCHLD) || child.Errno != 0 || child.Pid <= 0 {
			return ChildNoEventV2, fmt.Errorf("the original child supplied an unknown native wait observation. Try: retain its actual creation and wait owner")
		}
		switch child.Code {
		case 1, cldKilled, cldDumped:
			return ChildExitedV2, nil
		case 5: // CLD_STOPPED: a job-control stop, not a ptrace trap.
			return ChildStoppedV2, nil
		case 6: // CLD_CONTINUED.
			return ChildContinuedV2, nil
		default:
			return ChildNoEventV2, fmt.Errorf("the original child has an unsupported native stop or trace state. Try: preserve its current owner and inspect the original process")
		}
	}
}
