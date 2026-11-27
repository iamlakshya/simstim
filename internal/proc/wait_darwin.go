package proc

import (
	"errors"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// AwaitStatus waits until pid, a child of this process, has exited, and
// returns its wait status. It leaves the child unreaped: until its parent
// reaps it, its PID, and the process group that it leads, name nothing
// else. It watches the child with kqueue, which reaps nothing. ok is false
// when pid is not a child, or is reaped already.
func AwaitStatus(pid int) (ws syscall.WaitStatus, ok bool) {
	ws, ok, _ = AwaitStatusError(pid)
	return ws, ok
}

// AwaitStatusError is AwaitStatus with its native observation error retained.
// A false result never proves exit or permits an owner to reap before it has
// revoked I/O and dealt with the original, still-unreaped child.
func AwaitStatusError(pid int) (ws syscall.WaitStatus, ok bool, err error) {
	if pid <= 0 {
		return 0, false, unix.EINVAL
	}

	kq, err := unix.Kqueue()
	if err != nil {
		return pollStatusError(pid)
	}
	defer unix.Close(kq)
	change := []unix.Kevent_t{{Ident: uint64(pid), Filter: unix.EVFILT_PROC, //nolint:gosec // G115: a PID is not negative
		Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT | unix.NOTE_EXITSTATUS}}
	ev := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, change, ev, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return pollStatusError(pid)
		case n > 0 && ev[0].Flags&unix.EV_ERROR != 0:
			// The watch failed, as for a child that exited before it
			// began (ESRCH): its status waits, unreaped. The event's data
			// is the error, not a status.
			return pollStatusError(pid)
		case n > 0:
			return syscall.WaitStatus(ev[0].Data), true, nil //nolint:gosec // G115: the wait status fits
		}
	}
}

// pollStatusError waits for the child pid's exit with waitid, which reaps
// nothing with WNOWAIT, and returns its status.
func pollStatusError(pid int) (syscall.WaitStatus, bool, error) {
	for {
		ws, exited, err := peekStatus(pid)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil:
			return 0, false, err
		case exited:
			return ws, true, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// chldInfo is siginfo_t on macOS, as waitid fills it for a child.
type chldInfo struct {
	Signo, Errno, Code, Pid, UID, Status int32
	_                                    [80]byte
}

// peekStatus reads the status of the child pid, when it has exited,
// without a wait and without reaping it. x/sys has no waitid for macOS.
func peekStatus(pid int) (ws syscall.WaitStatus, exited bool, err error) {
	var info chldInfo
	const pPID = 1                                                                                // P_PID of idtype_t
	_, _, e := unix.Syscall6(unix.SYS_WAITID, pPID, uintptr(pid), uintptr(unsafe.Pointer(&info)), //nolint:gosec,staticcheck // G103, G115: waitid's siginfo_t, and a PID that is not negative. SA1019: x/sys has no waitid for macOS
		unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, 0, 0)
	if e != 0 {
		return 0, false, e
	}
	if int(info.Pid) != pid {
		return 0, false, nil
	}
	return waitStatus(info.Code, info.Status), true, nil
}
