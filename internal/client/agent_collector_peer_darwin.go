package client

import (
	"net"

	"golang.org/x/sys/unix"
)

func agentCollectorPeer(connection net.Conn) (int, uint32, error) {
	local, ok := connection.(*net.UnixConn)
	if !ok {
		return 0, 0, agentBindingProofError()
	}
	raw, err := local.SyscallConn()
	if err != nil {
		return 0, 0, agentBindingProofError()
	}
	var pid int
	var uid uint32
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		pid, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if peerErr != nil {
			return
		}
		credentials, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			peerErr = err
			return
		}
		uid = credentials.Uid
	}); err != nil || peerErr != nil || pid <= 0 {
		return 0, 0, agentBindingProofError()
	}
	return pid, uid, nil
}
