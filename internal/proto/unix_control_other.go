//go:build !linux

package proto

import (
	"errors"
	"net"
	"os"
)

// EnableUnixCredentialsV1 is available on supported native Linux hosts.
func EnableUnixCredentialsV1(*net.UnixConn) error {
	return errors.New("native Unix control requires Linux")
}

// ReceiveUnixControlV1 is available on supported native Linux hosts.
func ReceiveUnixControlV1(*net.UnixConn, []byte, int) (int, UnixControlV1, error) {
	return 0, UnixControlV1{}, errors.New("native Unix control requires Linux")
}

func ReceiveUnixControlRetainedV1(*net.UnixConn, []byte, int, *UnixControlRetentionV1) (int, UnixControlV1, error) {
	return 0, UnixControlV1{}, errors.New("native Unix control needs a supported Linux host")
}

// SendUnixControlV1 is available on supported native Linux hosts.
func SendUnixControlV1(*net.UnixConn, []byte, []*os.File) error {
	return errors.New("native Unix control requires Linux")
}
