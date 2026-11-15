//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ownedVMMEndpoint is constructed only from the actual child creation pidfd
// and process object in RunVMHelper. A path, PID or API reply cannot mint it.
// Its caller retains the descriptor until the owned child wait has joined.
type ownedVMMEndpoint struct {
	mu  sync.Mutex
	fd  int
	pid int
	uid uint32
}

func (o *ownedVMMEndpoint) live() error {
	if o == nil {
		return fmt.Errorf("the VMM has no retained native process owner")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fd < 0 || o.pid <= 0 {
		return fmt.Errorf("the VMM has no retained native process owner")
	}
	for {
		poll := []unix.PollFd{{Fd: int32(o.fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("check original VMM process lifetime: %w", err)
		}
		if n != 0 || poll[0].Revents != 0 {
			return fmt.Errorf("the original VMM process ended or its native lifetime is uncertain")
		}
		return nil
	}
}

func (o *ownedVMMEndpoint) close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fd < 0 {
		return nil
	}
	fd := o.fd
	o.fd = -1
	return unix.Close(fd)
}

func (o *ownedVMMEndpoint) dial(ctx context.Context, path string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := o.live(); err != nil {
		return nil, err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	local, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, fmt.Errorf("the private VMM endpoint is not a Unix stream")
	}
	raw, err := local.SyscallConn()
	if err != nil {
		connection.Close()
		return nil, err
	}
	var peer *unix.Ucred
	var nativeErr error
	err = raw.Control(func(fd uintptr) {
		peer, nativeErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err == nil {
		err = nativeErr
	}
	if err == nil && (peer == nil || int(peer.Pid) != o.pid || peer.Uid != o.uid) {
		err = fmt.Errorf("the private endpoint does not belong to the original owned VMM")
	}
	if err == nil {
		err = o.live()
	}
	if err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func (o *ownedVMMEndpoint) connectAPI(ctx context.Context, path string) (net.Conn, error) {
	for {
		connection, err := o.dial(ctx, path)
		if err == nil {
			return connection, nil
		}
		if err := o.live(); err != nil {
			return nil, err
		}
		if err := pauseVMConnect(ctx); err != nil {
			return nil, err
		}
	}
}

func pauseVMConnect(ctx context.Context) error {
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
