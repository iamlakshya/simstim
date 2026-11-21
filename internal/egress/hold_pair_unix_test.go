//go:build unix

package egress

import (
	"net"
	"os"
	"syscall"
	"testing"
)

func holdTransportPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	if os.Getenv("LINK_TEST_PIPE") != "" {
		return net.Pipe()
	}
	descriptors, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	connection := func(descriptor int) net.Conn {
		file := os.NewFile(uintptr(descriptor), "owned-hold-link")
		defer file.Close()
		conn, err := net.FileConn(file)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	return connection(descriptors[0]), connection(descriptors[1])
}
