//go:build linux

package guest

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type vmNLMessage struct {
	kind  uint16
	flags uint16
	data  []byte
}
type vmNLReply struct {
	kind uint16
	data []byte
}

func vmNLAttr(kind uint16, data []byte) []byte {
	length := 4 + len(data)
	out := make([]byte, (length+3)&^3)
	binary.NativeEndian.PutUint16(out, uint16(length))
	binary.NativeEndian.PutUint16(out[2:], kind)
	copy(out[4:], data)
	return out
}
func vmNLString(s string) []byte { return append([]byte(s), 0) }
func vmNLU32(v uint32) []byte {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, v)
	return data
}
func vmNFU32(v uint32) []byte {
	data := make([]byte, 4)
	binary.BigEndian.PutUint32(data, v)
	return data
}
func vmNLJoin(values ...[]byte) []byte {
	var out []byte
	for _, value := range values {
		out = append(out, value...)
	}
	return out
}

func vmNLAttrs(data []byte) (map[uint16][]byte, error) {
	attrs := map[uint16][]byte{}
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, fmt.Errorf("short guest netlink attribute")
		}
		size := int(binary.NativeEndian.Uint16(data))
		kind := binary.NativeEndian.Uint16(data[2:]) & 0x3fff
		if size < 4 || size > len(data) || (size+3)&^3 > len(data) {
			return nil, fmt.Errorf("invalid guest netlink attribute length")
		}
		attrs[kind] = append([]byte(nil), data[4:size]...)
		data = data[(size+3)&^3:]
	}
	return attrs, nil
}

// vmNetlink owns a private guest-only kernel socket. It performs no namespace
// or host probe: only the already verified VM PID1 setup calls it.
func vmNetlink(protocol int, messages []vmNLMessage) ([]vmNLReply, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, protocol)
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "guest-netlink")
	defer file.Close()
	if err := file.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var packet []byte
	wanted := map[uint32]bool{}
	sent := map[uint32]bool{}
	for i, message := range messages {
		sequence := uint32(i + 1)
		size := 16 + len(message.data)
		header := make([]byte, (size+3)&^3)
		binary.NativeEndian.PutUint32(header, uint32(size))
		binary.NativeEndian.PutUint16(header[4:], message.kind)
		binary.NativeEndian.PutUint16(header[6:], message.flags)
		binary.NativeEndian.PutUint32(header[8:], sequence)
		copy(header[16:], message.data)
		packet = append(packet, header...)
		sent[sequence] = true
		if message.flags&unix.NLM_F_ACK != 0 {
			wanted[sequence] = true
		}
	}
	var sendErr error
	err = raw.Write(func(socket uintptr) bool {
		sendErr = unix.Sendto(int(socket), packet, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
		return sendErr != unix.EAGAIN && sendErr != unix.EWOULDBLOCK
	})
	if err != nil {
		return nil, err
	}
	if sendErr != nil {
		return nil, sendErr
	}
	var replies []vmNLReply
	buffer := make([]byte, 64<<10)
	for len(wanted) > 0 {
		var n int
		var peer unix.Sockaddr
		var receiveErr error
		err = raw.Read(func(socket uintptr) bool {
			n, peer, receiveErr = unix.Recvfrom(int(socket), buffer, 0)
			return receiveErr != unix.EAGAIN && receiveErr != unix.EWOULDBLOCK
		})
		if err != nil {
			return nil, err
		}
		if receiveErr != nil {
			return nil, receiveErr
		}
		kernel, ok := peer.(*unix.SockaddrNetlink)
		if !ok || kernel.Pid != 0 {
			return nil, fmt.Errorf("guest netlink reply did not come from its kernel")
		}
		parsed, err := syscall.ParseNetlinkMessage(buffer[:n])
		if err != nil {
			return nil, err
		}
		for _, message := range parsed {
			if !sent[message.Header.Seq] {
				continue
			}
			if message.Header.Type == unix.NLMSG_ERROR {
				if len(message.Data) < 4 {
					return nil, fmt.Errorf("short guest netlink acknowledgement")
				}
				code := int32(binary.NativeEndian.Uint32(message.Data))
				if code != 0 {
					return nil, unix.Errno(-code)
				}
				delete(wanted, message.Header.Seq)
				continue
			}
			replies = append(replies, vmNLReply{kind: message.Header.Type, data: append([]byte(nil), message.Data...)})
		}
	}
	return replies, nil
}
