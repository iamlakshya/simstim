//go:build linux

package proto

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func unixReceiveConnectionIdentityV1(connection *net.UnixConn) (string, error) {
	if connection == nil {
		return "", fmt.Errorf("native receive connection is missing")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return "", err
	}
	var stat unix.Stat_t
	var observed error
	err = raw.Control(func(fd uintptr) {
		observed = unix.Fstat(int(fd), &stat)
		if observed == nil && stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
			observed = fmt.Errorf("native receive connection is not its original socket")
		}
	})
	if err != nil || observed != nil {
		return "", errors.Join(err, observed)
	}
	return fmt.Sprintf("unix:%v:%v", stat.Dev, stat.Ino), nil
}
