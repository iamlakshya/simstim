package host

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerOf reads the process at the other end of conn, a Unix socket. The
// kernel records the peer's credentials when it calls connect, so a later
// change of the peer's own does not count (unix(7)). Where the kernel has
// SO_PEERPIDFD (Linux 6.5), the peer also comes with a pidfd: it refers to
// that one process, and never to another that reuses its pid.
func peerOf(conn net.Conn) (peer, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return peer{}, errors.New("not a Unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return peer{}, fmt.Errorf("read the peer of a session socket: %w", err)
	}
	var cred *unix.Ucred
	var credErr error
	pidfd := -1
	err = raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if credErr != nil {
			return
		}
		n, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		switch use, refuse := pidfdAnswer(err); {
		case use:
			pidfd = n
		case refuse:
			credErr = fmt.Errorf("the peer's pidfd: %w", err)
		}
	})
	if err == nil {
		err = credErr
	}
	if err != nil {
		if pidfd >= 0 {
			_ = unix.Close(pidfd)
		}
		return peer{}, fmt.Errorf("read the peer of a session socket: %w", err)
	}
	p := peer{pid: int(cred.Pid), uid: int(cred.Uid)}
	if pidfd >= 0 {
		p.alive = func() bool { return pidfdAlive(unix.PidfdSendSignal(pidfd, 0, nil, 0)) }
		p.done = func() { _ = unix.Close(pidfd) }
	}
	return p, nil
}
