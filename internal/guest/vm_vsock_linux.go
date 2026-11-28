//go:build linux

package guest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const (
	evKeyType          = 1   // EV_KEY
	keyPowerCode       = 116 // KEY_POWER (ACPI power button)
	inputEventBytes    = 24  // 64-bit Linux struct input_event size
	genlIDCtrl         = 0x10
	ctrlCmdGetFamily   = 3
	ctrlAttrFamilyName = 2
	ctrlAttrMcastGrps  = 7
	ctrlAttrMcastName  = 1
	ctrlAttrMcastID    = 2
)

// watchACPIPowerButton subscribes to Linux's Generic Netlink "acpi_event"
// multicast group ("acpi_mc_group") so ACPI power-button presses from
// ch-remote power-button work even on kernels built without CONFIG_INPUT_EVDEV.
func watchACPIPowerButton(onPower func()) func() {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_GENERIC)
	if err != nil {
		return func() {}
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return func() {}
	}
	groupID, ok := resolveGenlMulticastGroup(fd, "acpi_event", "acpi_mc_group")
	if !ok {
		_ = unix.Close(fd)
		return func() {}
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_NETLINK, unix.NETLINK_ADD_MEMBERSHIP, int(groupID)); err != nil {
		_ = unix.Close(fd)
		return func() {}
	}
	_ = unix.SetNonblock(fd, true)
	f := os.NewFile(uintptr(fd), "acpi-netlink")
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := f.Read(buf)
			if rerr != nil {
				return
			}
			if bytes.Contains(buf[:n], []byte("button/power")) {
				onPower()
				return
			}
		}
	}()
	return func() { _ = f.Close() }
}

func resolveGenlMulticastGroup(fd int, familyName, groupName string) (uint32, bool) {
	nameBytes := append([]byte(familyName), 0)
	attrLen := 4 + len(nameBytes)
	paddedAttrLen := (attrLen + 3) &^ 3
	msgLen := 16 + 4 + paddedAttrLen

	req := make([]byte, msgLen)
	binary.LittleEndian.PutUint32(req[0:4], uint32(msgLen)) //nolint:gosec // G115: small fixed message length
	binary.LittleEndian.PutUint16(req[4:6], genlIDCtrl)
	binary.LittleEndian.PutUint16(req[6:8], unix.NLM_F_REQUEST)
	binary.LittleEndian.PutUint32(req[8:12], 1)
	req[16] = ctrlCmdGetFamily
	req[17] = 1
	binary.LittleEndian.PutUint16(req[20:22], uint16(attrLen)) //nolint:gosec // G115: small family name attr
	binary.LittleEndian.PutUint16(req[22:24], ctrlAttrFamilyName)
	copy(req[24:], nameBytes)

	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return 0, false
	}
	resp := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, resp, 0)
	if err != nil || n < 20 {
		return 0, false
	}
	// Walk top-level attributes starting after nlmsghdr (16) + genlmsghdr (4).
	for off := 20; off+4 <= n; {
		al := int(binary.LittleEndian.Uint16(resp[off : off+2]))
		at := binary.LittleEndian.Uint16(resp[off+2:off+4]) & 0x3fff
		if al < 4 || off+al > n {
			break
		}
		if at == ctrlAttrMcastGrps {
			if id, ok := findMcastGroupID(resp[off+4:off+al], groupName); ok {
				return id, true
			}
		}
		off += (al + 3) &^ 3
	}
	return 0, false
}

func findMcastGroupID(data []byte, wantName string) (uint32, bool) {
	for off := 0; off+4 <= len(data); {
		el := int(binary.LittleEndian.Uint16(data[off : off+2]))
		if el < 4 || off+el > len(data) {
			break
		}
		entry := data[off+4 : off+el]
		var name string
		var id uint32
		var hasID bool
		for sub := 0; sub+4 <= len(entry); {
			sl := int(binary.LittleEndian.Uint16(entry[sub : sub+2]))
			st := binary.LittleEndian.Uint16(entry[sub+2:sub+4]) & 0x3fff
			if sl < 4 || sub+sl > len(entry) {
				break
			}
			val := entry[sub+4 : sub+sl]
			switch st {
			case ctrlAttrMcastName:
				name = string(bytes.TrimRight(val, "\x00"))
			case ctrlAttrMcastID:
				if len(val) >= 4 {
					id = binary.LittleEndian.Uint32(val[:4])
					hasID = true
				}
			}
			sub += (sl + 3) &^ 3
		}
		if hasID && (name == wantName || wantName == "") {
			return id, true
		}
		off += (el + 3) &^ 3
	}
	return 0, false
}

// watchInputPowerButton watches /dev/input/event* for EV_KEY/KEY_POWER on
// kernels built with CONFIG_INPUT_EVDEV=y.
func watchInputPowerButton(onPower func()) func() {
	matches, _ := filepath.Glob("/dev/input/event*")
	files := make([]*os.File, 0, len(matches))
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		files = append(files, f)
		go func(evFile *os.File) {
			var buf [inputEventBytes]byte
			for {
				if _, err := io.ReadFull(evFile, buf[:]); err != nil {
					return
				}
				evType := binary.LittleEndian.Uint16(buf[16:18])
				evCode := binary.LittleEndian.Uint16(buf[18:20])
				evVal := int32(binary.LittleEndian.Uint32(buf[20:24])) //nolint:gosec // G115: Linux input_event value is signed int32
				if evType == evKeyType && evCode == keyPowerCode && evVal == 1 {
					onPower()
					return
				}
			}
		}(f)
	}
	return func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
}

// vsockListener wraps a Linux AF_VSOCK listening socket using Go's runtime
// netpoller via os.NewFile + SyscallConn.
type vsockListener struct {
	file *os.File
	port uint32
}

func listenVsock(port uint32) (*vsockListener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create AF_VSOCK socket: %w", err)
	}
	sa := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}
	if err := unix.Bind(fd, sa); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("bind AF_VSOCK port %d: %w", port, err)
	}
	if err := unix.Listen(fd, 8); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("listen AF_VSOCK port %d: %w", port, err)
	}
	return &vsockListener{
		file: os.NewFile(uintptr(fd), "vsock-listen"),
		port: port,
	}, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	rc, err := l.file.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("vsock syscall conn: %w", err)
	}
	var (
		nfd      int
		remote   unix.Sockaddr
		acceptOp error
	)
	err = rc.Read(func(fd uintptr) bool {
		nfd, remote, acceptOp = unix.Accept4(int(fd), unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC)
		return !errors.Is(acceptOp, unix.EAGAIN) && !errors.Is(acceptOp, unix.EWOULDBLOCK)
	})
	if err != nil {
		return nil, fmt.Errorf("wait on vsock listener: %w", err)
	}
	if acceptOp != nil {
		return nil, fmt.Errorf("accept vsock connection: %w", acceptOp)
	}
	var peerCID, peerPort uint32
	if svm, ok := remote.(*unix.SockaddrVM); ok {
		peerCID, peerPort = svm.CID, svm.Port
	}
	return &vsockConn{
		file:   os.NewFile(uintptr(nfd), "vsock-conn"),
		local:  vsockAddr{cid: unix.VMADDR_CID_ANY, port: l.port},
		remote: vsockAddr{cid: peerCID, port: peerPort},
	}, nil
}

func (l *vsockListener) Close() error {
	if l.file == nil {
		return nil
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close vsock listener: %w", err)
	}
	return nil
}

type vsockAddr struct {
	cid  uint32
	port uint32
}

func (a vsockAddr) Network() string { return "vsock" }
func (a vsockAddr) String() string {
	return "vsock:" + strconv.FormatUint(uint64(a.cid), 10) + ":" + strconv.FormatUint(uint64(a.port), 10)
}

type vsockConn struct {
	file   *os.File
	local  vsockAddr
	remote vsockAddr
}

func (c *vsockConn) Read(b []byte) (int, error) {
	n, err := c.file.Read(b)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}
		return n, fmt.Errorf("read vsock: %w", err)
	}
	return n, nil
}

func (c *vsockConn) Write(b []byte) (int, error) {
	n, err := c.file.Write(b)
	if err != nil {
		return n, fmt.Errorf("write vsock: %w", err)
	}
	return n, nil
}

func (c *vsockConn) Close() error {
	if err := c.file.Close(); err != nil {
		return fmt.Errorf("close vsock: %w", err)
	}
	return nil
}

func (c *vsockConn) LocalAddr() net.Addr  { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }

func (c *vsockConn) SetDeadline(t time.Time) error {
	if err := c.file.SetDeadline(t); err != nil {
		return fmt.Errorf("set vsock deadline: %w", err)
	}
	return nil
}

func (c *vsockConn) SetReadDeadline(t time.Time) error {
	if err := c.file.SetReadDeadline(t); err != nil {
		return fmt.Errorf("set vsock read deadline: %w", err)
	}
	return nil
}

func (c *vsockConn) SetWriteDeadline(t time.Time) error {
	if err := c.file.SetWriteDeadline(t); err != nil {
		return fmt.Errorf("set vsock write deadline: %w", err)
	}
	return nil
}
