//go:build !unix

package link

import (
	"net"
	"testing"
)

// socketPair stands in for a pair of Unix sockets where there are none.
func socketPair(tb testing.TB) (net.Conn, net.Conn) {
	tb.Helper()
	return pipePair(tb)
}
