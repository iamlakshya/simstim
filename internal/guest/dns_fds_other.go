//go:build !linux

package guest

import (
	"errors"
	"syscall"
)

func isListeningTCPSocket(fd int) bool {
	accepts, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	if err == nil {
		return accepts != 0
	}
	if !errors.Is(err, syscall.ENOPROTOOPT) {
		return false
	}
	// macOS does not implement getsockopt(SO_ACCEPTCONN) and returns ENOPROTOOPT.
	// Reject connected sockets via getpeername and require a bound local address;
	// net.FileListener validates the adopted listener immediately after.
	if _, err := syscall.Getpeername(fd); !errors.Is(err, syscall.ENOTCONN) {
		return false
	}
	sa, err := syscall.Getsockname(fd)
	return err == nil && sa != nil
}
