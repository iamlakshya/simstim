package proc

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// chldInfo is siginfo_t as waitid fills it for a child, on 64-bit Linux:
// unix.Siginfo with the fields of SIGCHLD named.
type chldInfo struct {
	Signo, Errno, Code, _ int32
	Pid, UID, Status      int32
	_                     [100]byte
}

// AwaitStatus waits until pid, a child of this process, has exited, and
// returns its wait status. It leaves the child unreaped: until its parent
// reaps it, its PID, and the process group that it leads, name nothing
// else. ok is false when pid is not a child, or is reaped already.
func AwaitStatus(pid int) (ws syscall.WaitStatus, ok bool) {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, false
		}
		c := (*chldInfo)(unsafe.Pointer(&info)) //nolint:gosec // G103: the same 128 bytes, with SIGCHLD's fields named
		return waitStatus(c.Code, c.Status), true
	}
}
