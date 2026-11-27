//go:build unix

package link

import (
	"net"
	"os"
	"syscall"
	"testing"
)

// socketPair makes a connected pair of Unix sockets, as the process wall
// gives the guest init. With LINK_TEST_PIPE set, it makes a net.Pipe, as on
// Windows: a pipe with no buffer, which finds a test that needs one.
func socketPair(tb testing.TB) (net.Conn, net.Conn) {
	tb.Helper()
	if os.Getenv("LINK_TEST_PIPE") != "" {
		return pipePair(tb)
	}
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		tb.Fatal(err)
	}
	conn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "link")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			tb.Fatal(err)
		}
		return c
	}
	a, b := conn(fds[0]), conn(fds[1])
	tb.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
