//go:build !linux

package host

import (
	"errors"
	"net"
)

// peerOf has no peer to read here. Hosts run Linux. Elsewhere, as on a
// developer's Mac, sessions have no systemd unit, so the holder keeps the
// role check alone and never asks for the peer.
func peerOf(net.Conn) (peer, error) {
	return peer{}, errors.New("this system gives no peer's cgroup")
}
