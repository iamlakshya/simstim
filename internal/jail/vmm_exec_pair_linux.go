//go:build linux

package jail

import (
	"encoding/binary"
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// UNIX_DIAG is a local read-only kernel query. It proves the actual opposite
// endpoint of the original socket pair; peer credentials alone describe the
// process that made a socket and do not establish this relation.
func checkVMMControlPairV1(conn *net.UnixConn, child *os.File) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var checkErr error
	err = raw.Control(func(fd uintptr) { checkErr = checkVMMControlPairFDV1(int(fd), int(child.Fd())) })
	return errors.Join(err, checkErr)
}
func checkVMMControlPairFDV1(parent, child int) error {
	var first, second unix.Stat_t
	if err := errors.Join(unix.Fstat(parent, &first), unix.Fstat(child, &second)); err != nil {
		return err
	}
	if first.Mode&unix.S_IFMT != unix.S_IFSOCK || second.Mode&unix.S_IFMT != unix.S_IFSOCK || first.Ino == second.Ino || first.Dev != second.Dev || first.Ino > 1<<32-1 || second.Ino > 1<<32-1 {
		return vmmModeFailureV1("the original VMM control endpoints have another native identity")
	}
	for _, fd := range []int{parent, child} {
		family, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
		if err != nil {
			return err
		}
		kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		if err != nil {
			return err
		}
		if family != unix.AF_UNIX || kind != unix.SOCK_STREAM {
			return vmmModeFailureV1("the original VMM control is not a native Unix stream pair")
		}
		for _, query := range []uint{unix.SIOCINQ, unix.TIOCOUTQ} {
			queued, err := unix.IoctlRetInt(fd, query)
			if err != nil {
				return err
			}
			if queued != 0 {
				return vmmModeFailureV1("the original VMM control was not bound at byte zero")
			}
		}
	}
	peer, err := vmmUnixPeerInodeV1(uint32(first.Ino))
	if err != nil {
		return err
	}
	if uint64(peer) != second.Ino {
		return vmmModeFailureV1("the actual host control has another native peer")
	}
	peer, err = vmmUnixPeerInodeV1(uint32(second.Ino))
	if err != nil {
		return err
	}
	if uint64(peer) != first.Ino {
		return vmmModeFailureV1("the actual child control has another native peer")
	}
	var afterFirst, afterSecond unix.Stat_t
	if err := errors.Join(unix.Fstat(parent, &afterFirst), unix.Fstat(child, &afterSecond)); err != nil {
		return err
	}
	if first.Dev != afterFirst.Dev || first.Ino != afterFirst.Ino || second.Dev != afterSecond.Dev || second.Ino != afterSecond.Ino {
		return vmmModeFailureV1("the native VMM pair changed during binding")
	}
	return nil
}
func vmmUnixPeerInodeV1(inode uint32) (uint32, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_SOCK_DIAG)
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	timeout := unix.NsecToTimeval(2_000_000_000)
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout); err != nil {
		return 0, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}
	address, err := unix.Getsockname(fd)
	if err != nil {
		return 0, err
	}
	local, ok := address.(*unix.SockaddrNetlink)
	if !ok {
		return 0, vmmModeFailureV1("the native socket diagnostic has another address family")
	}
	// Linux UAPI unix_diag_req, exactly 24 bytes after nlmsghdr. Query the
	// one original inode, request only PEER; both cookies are INET_DIAG_NOCOOKIE.
	request := make([]byte, 40)
	native := binary.NativeEndian
	native.PutUint32(request[:4], 40)
	native.PutUint16(request[4:6], unix.SOCK_DIAG_BY_FAMILY)
	native.PutUint16(request[6:8], unix.NLM_F_REQUEST)
	native.PutUint32(request[8:12], 1)
	native.PutUint32(request[12:16], local.Pid)
	request[16] = unix.AF_UNIX
	native.PutUint32(request[20:24], ^uint32(0))
	native.PutUint32(request[24:28], inode)
	native.PutUint32(request[28:32], 1<<2)
	native.PutUint32(request[32:36], ^uint32(0))
	native.PutUint32(request[36:40], ^uint32(0))
	if err := unix.Sendto(fd, request, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, err
	}
	buffer := make([]byte, 4096)
	n, _, flags, from, err := unix.Recvmsg(fd, buffer, nil, 0)
	if err != nil {
		return 0, err
	}
	kernel, ok := from.(*unix.SockaddrNetlink)
	if !ok || kernel.Pid != 0 || flags&unix.MSG_TRUNC != 0 {
		return 0, vmmModeFailureV1("the socket diagnostic is not a complete native kernel response")
	}
	return parseVMMUnixPeerV1(buffer[:n], inode, local.Pid)
}
func parseVMMUnixPeerV1(data []byte, inode, port uint32) (uint32, error) {
	native := binary.NativeEndian
	if len(data) < 32 || int(native.Uint32(data[:4])) != len(data) || native.Uint16(data[4:6]) != unix.SOCK_DIAG_BY_FAMILY || native.Uint32(data[8:12]) != 1 || native.Uint32(data[12:16]) != port || data[16] != unix.AF_UNIX || data[17] != unix.SOCK_STREAM || native.Uint32(data[20:24]) != inode {
		return 0, vmmModeFailureV1("the exact native socket diagnostic reply has another role or identity")
	}
	peer := uint32(0)
	for offset := 32; offset < len(data); {
		if len(data)-offset < 4 {
			return 0, vmmModeFailureV1("the native peer attribute is truncated")
		}
		size := int(native.Uint16(data[offset : offset+2]))
		kind := native.Uint16(data[offset+2 : offset+4])
		aligned := (size + 3) &^ 3
		if size < 4 || size > len(data)-offset || aligned > len(data)-offset {
			return 0, vmmModeFailureV1("the native peer attribute has an invalid bound")
		}
		if kind == 3 {
			if size != 8 || peer != 0 {
				return 0, vmmModeFailureV1("the exact native peer is duplicate or malformed")
			}
			peer = native.Uint32(data[offset+4 : offset+8])
		}
		offset += aligned
	}
	if peer == 0 {
		return 0, vmmModeFailureV1("the original socket has no exact native peer inode")
	}
	return peer, nil
}
