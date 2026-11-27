//go:build !unix

package egress

import (
	"net"
	"testing"
)

func holdTransportPair(t *testing.T) (net.Conn, net.Conn) { t.Helper(); return net.Pipe() }
