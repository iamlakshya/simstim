//go:build linux

package guest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const vmGuestPort uint32 = 1024

type vmSocketAddress struct{ cid, port uint32 }

func (a vmSocketAddress) Network() string { return "vsock" }
func (a vmSocketAddress) String() string  { return fmt.Sprintf("%d:%d", a.cid, a.port) }

type vmSocketConn struct {
	*os.File
	local, remote vmSocketAddress
}

func (c *vmSocketConn) LocalAddr() net.Addr  { return c.local }
func (c *vmSocketConn) RemoteAddr() net.Addr { return c.remote }

// Nonblocking privately created FDs enter Go1.26's runtime poller through
// os.NewFile. File deadlines/Close interrupt owned IO; no blocking custom
// descriptor loop or external module is needed. No FD/status of a caller is
// mutated and accepted streams are never inherited by a child exec.
func vmSocketFile(fd int, name string) (*os.File, error) {
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		unix.Close(fd)
		return nil, fmt.Errorf("invalid guest vsock descriptor")
	}
	if err := file.SetDeadline(time.Time{}); err != nil {
		file.Close()
		return nil, fmt.Errorf("guest vsock is not deadline-capable: %w", err)
	}
	return file, nil
}

func listenVMGuest() (*os.File, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open guest vsock: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			unix.Close(fd)
		}
	}()
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: vmGuestPort}); err != nil {
		return nil, fmt.Errorf("bind guest init vsock: %w", err)
	}
	if err := unix.Listen(fd, 1); err != nil {
		return nil, fmt.Errorf("listen for the one supervisor link: %w", err)
	}
	failed = false
	return vmSocketFile(fd, "guest-init-vsock")
}

func acceptVMGuest(ctx context.Context, listener *os.File) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := listener.SyscallConn()
	if err != nil {
		return nil, err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { listener.Close(); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	for {
		fd := -1
		var address unix.Sockaddr
		var acceptErr error
		err = raw.Read(func(socket uintptr) bool {
			fd, address, acceptErr = unix.Accept4(int(socket), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
			return !errors.Is(acceptErr, unix.EAGAIN) && !errors.Is(acceptErr, unix.EWOULDBLOCK)
		})
		if ctx.Err() != nil {
			if fd >= 0 {
				unix.Close(fd)
			}
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("accept guest supervisor link: %w", err)
		}
		if acceptErr != nil {
			return nil, fmt.Errorf("accept guest supervisor link: %w", acceptErr)
		}
		peer, ok := address.(*unix.SockaddrVM)
		if !ok || peer.CID != unix.VMADDR_CID_HOST {
			unix.Close(fd)
			continue
		}
		file, err := vmSocketFile(fd, "guest-supervisor-link")
		if err != nil {
			return nil, err
		}
		return &vmSocketConn{File: file, local: vmSocketAddress{unix.VMADDR_CID_ANY, vmGuestPort}, remote: vmSocketAddress{peer.CID, peer.Port}}, nil
	}
}
