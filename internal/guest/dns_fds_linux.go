//go:build linux

package guest

import "syscall"

func isListeningTCPSocket(fd int) bool {
	accepts, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	return err == nil && accepts != 0
}
