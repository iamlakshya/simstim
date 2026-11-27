//go:build linux

package process

import (
	"errors"
	"fmt"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

func copyControlV1(runtime *sandbox) (*CopyOwnerV1, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "original-copy-owner")
	child := os.NewFile(uintptr(fds[1]), "launcher-copy-owner")
	connection, err := net.FileConn(parent)
	closeErr := parent.Close()
	if err != nil || closeErr != nil {
		if connection != nil {
			_ = connection.Close()
		}
		_ = child.Close()
		return nil, nil, fmt.Errorf("adopt private copy controller: %w", errors.Join(err, closeErr))
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		_ = child.Close()
		return nil, nil, fmt.Errorf("private copy controller is not Unix")
	}
	owner := &CopyOwnerV1{native: &copyOwnerNativeV1{runtime: runtime, conn: conn, mounts: make(map[uint32]*copyMountNativeV1), publisher: -1}}
	return owner, child, nil
}
