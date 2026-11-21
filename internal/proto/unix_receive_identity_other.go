//go:build !linux

package proto

import (
	"fmt"
	"net"
)

func unixReceiveConnectionIdentityV1(*net.UnixConn) (string, error) {
	return "", fmt.Errorf("native receive provenance requires Linux")
}
