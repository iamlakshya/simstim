package link

import (
	"net"
	"testing"
)

// pipePair makes a net.Pipe, whose writes wait until the other end reads
// them.
func pipePair(tb testing.TB) (net.Conn, net.Conn) {
	tb.Helper()
	a, b := net.Pipe()
	tb.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
