package proctest

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Handle is a hold on one process: on macOS, a child of this process that
// is not reaped yet, whose PID and group name nothing else.
type Handle struct {
	pid int
}

// pin holds pid only when it is this process's child, not reaped yet:
// macOS has no pidfd.
func pin(pid int, word string) *Handle {
	if !unreapedChild(pid) {
		return nil
	}
	out, err := exec.CommandContext(context.Background(), "ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil || !strings.Contains(string(out), word) {
		return nil
	}
	return &Handle{pid: pid}
}

// signal checks that the process is still an unreaped child, then sends
// the signal. Only this process reaps its children, so nothing reaps it
// between the check and the signal, unless the test reaps it itself at
// that moment, on another goroutine: a test does not.
func (h *Handle) signal(sig syscall.Signal) error {
	if h == nil || h.pid <= 0 || !unreapedChild(h.pid) {
		return ErrNoHold
	}
	if err := syscall.Kill(h.pid, sig); err != nil {
		return ErrNoHold
	}
	return nil
}

func (h *Handle) close() {
	if h != nil {
		h.pid = 0
	}
}

// unreapedChild reports whether pid is a child of this process that is not
// reaped yet: waitid with WNOWAIT and WNOHANG reaps nothing, and fails
// with ECHILD for any other process.
func unreapedChild(pid int) bool {
	var info [104]byte                                                                            // siginfo_t
	const pPID = 1                                                                                // P_PID of idtype_t
	_, _, e := unix.Syscall6(unix.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info)), //nolint:staticcheck // SA1019: x/sys has no waitid for macOS
		unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, 0, 0)
	return e == 0
}
