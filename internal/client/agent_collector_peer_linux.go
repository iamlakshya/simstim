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
	var peer *unix.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || peerErr != nil || peer == nil {
		return 0, 0, agentBindingProofError()
	}
	return int(peer.Pid), peer.Uid, nil
}
