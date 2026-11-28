//go:build !linux

package guest

import (
	"errors"
	"net"
	"net/netip"
)

func originalDst(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("SO_ORIGINAL_DST is only supported on Linux")
}
