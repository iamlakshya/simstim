//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// Capture directly from this original manager transport. Retain every actual
// kernel handle on manager before any later error; generic peerOf's temporary
// close callback cannot supply a first-close retirement record for us.
func (manager *nativeUserManagerV1) captureOriginalPeerV4() error {
	if manager == nil || manager.connection == nil || manager.peer != nil {
		return sessionLeaseError("the manager peer capture has no fresh original transport")
	}
	raw, err := manager.connection.SyscallConn()
	if err != nil {
		return err
	}
	var credentials *unix.Ucred
	var native error
	err = raw.Control(func(fd uintptr) {
		credentials, native = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if native != nil {
			return
		}
		pidfd, err := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		switch use, refuse := pidfdAnswer(err); {
		case use:
			manager.peer = &nativeStartupPeerV1{pid: int(credentials.Pid), uid: credentials.Uid, pidfd: pidfd}
		case refuse:
			native = err
		}
	})
	if err != nil || native != nil {
		return errors.Join(err, native)
	}
	if credentials == nil || credentials.Pid <= 0 || credentials.Uid != uint32(os.Getuid()) {
		return sessionLeaseError("the native manager connector belongs to another original account")
	}
	if manager.peer == nil {
		fd, err := unix.PidfdOpen(int(credentials.Pid), 0)
		if err != nil {
			return err
		}
		manager.peer = &nativeStartupPeerV1{pid: int(credentials.Pid), uid: credentials.Uid, pidfd: fd}
	}
	process, err := os.OpenRoot(filepath.Join(procRoot, strconv.Itoa(int(credentials.Pid))))
	if process != nil {
		manager.peer.proc = process
	}
	if err != nil {
		return err
	}
	return manager.peer.Check()
}
