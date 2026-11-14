//go:build linux

package guest

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux netfilter IPv4/IPv6 UAPI defines option80 as SO_ORIGINAL_DST. The
// private accepted TCP fd is observed through SyscallConn.Control; no caller
// status flags/OFD or arbitrary descriptor is changed.
func vmOriginalDestination(conn *net.TCPConn) (netip.AddrPort, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var result netip.AddrPort
	var operationErr error
	err = raw.Control(func(fd uintptr) {
		for _, level := range []int{unix.SOL_IP, unix.SOL_IPV6} {
			var address [128]byte
			length := uint32(len(address))
			_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, fd, uintptr(level), 80, uintptr(unsafe.Pointer(&address[0])), uintptr(unsafe.Pointer(&length)), 0)
			runtime.KeepAlive(&address)
			if errno != 0 {
				operationErr = errno
				continue
			}
			if length > uint32(len(address)) {
				operationErr = fmt.Errorf("original guest address exceeds its buffer")
				continue
			}
			result, operationErr = vmDecodeOriginalDestination(address[:length])
			if operationErr == nil {
				return
			}
		}
	})
	if err != nil {
		return netip.AddrPort{}, err
	}
	return result, operationErr
}

func vmDecodeOriginalDestination(address []byte) (netip.AddrPort, error) {
	if len(address) < 8 {
		return netip.AddrPort{}, fmt.Errorf("short original guest address")
	}
	port := binary.BigEndian.Uint16(address[2:4])
	if port == 0 {
		return netip.AddrPort{}, fmt.Errorf("zero original guest port")
	}
	switch binary.NativeEndian.Uint16(address) {
	case unix.AF_INET:
		var value [4]byte
		copy(value[:], address[4:8])
		return netip.AddrPortFrom(netip.AddrFrom4(value), port), nil
	case unix.AF_INET6:
		if len(address) < 28 {
			return netip.AddrPort{}, fmt.Errorf("short original IPv6 guest address")
		}
		var value [16]byte
		copy(value[:], address[8:24])
		return netip.AddrPortFrom(netip.AddrFrom16(value).Unmap(), port), nil
	default:
		return netip.AddrPort{}, fmt.Errorf("unsupported original guest address family")
	}
}
