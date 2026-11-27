package proctest

import (
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Handle is a hold on one process: a pidfd, which names that process
// whoever its parent is.
type Handle struct {
	fd int
}

// pin opens a pidfd for pid, then checks the command line. The process is
// alive before and after the check, so the command line was its own.
func pin(pid int, word string) *Handle {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil
	}
	h := &Handle{fd: fd}
	ok := h.signal(0) == nil
	if ok {
		cmd, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		ok = err == nil && strings.Contains(string(cmd), word) && h.signal(0) == nil
	}
	if !ok {
		h.close()
		return nil
	}
	return h
}

func (h *Handle) signal(sig syscall.Signal) error {
	if h == nil || h.fd < 0 {
		return ErrNoHold
	}
	if err := unix.PidfdSendSignal(h.fd, sig, nil, 0); err != nil {
		return ErrNoHold
	}
	return nil
}

func (h *Handle) close() {
	if h != nil && h.fd >= 0 {
		_ = unix.Close(h.fd)
		h.fd = -1
	}
}

// unreapedChild reports whether pid is a child of this process that is not
// reaped yet: waitid with WNOWAIT and WNOHANG reaps nothing, and fails
// with ECHILD for any other process.
func unreapedChild(pid int) bool {
	var info unix.Siginfo
	return unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil) == nil
}
