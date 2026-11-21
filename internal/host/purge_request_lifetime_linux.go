//go:build linux

package host

import (
	"context"
	"errors"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// This observer never reads a protocol byte. The sole StartupLine reader
// keeps framing and SCM ownership. A socket half-close or original pidfd exit
// cancels this request, never the transaction's independent owner lifetime.
func purgeRequestLifetimeV3(parent context.Context, line *StartupLineV1, peer *purgeRequestPeerV3) (context.Context, func(), error) {
	if parent == nil || line == nil || peer == nil || peer.native == nil || line.nativeRetirement == nil {
		return nil, nil, placementError("the actual purge request has no original transport lifetime")
	}
	if err := peer.Check(parent); err != nil {
		return nil, nil, err
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return nil, nil, placementError("the actual purge request has no native Unix stream")
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, nil, err
	}
	socket := -1
	var duplicateErr error
	if err := raw.Control(func(fd uintptr) { socket, duplicateErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3) }); err != nil {
		return nil, nil, err
	}
	if duplicateErr != nil {
		return nil, nil, duplicateErr
	}
	socketFile := os.NewFile(uintptr(socket), "actual-purge-request-socket-watch")
	pool := line.nativeRetirement
	peer.native.mu.Lock()
	if peer.native.closed {
		peer.native.mu.Unlock()
		return nil, nil, errors.Join(placementError("the original purge connector ended"), pool.closeFilesV4([]*os.File{socketFile}))
	}
	process, err := unix.FcntlInt(uintptr(peer.native.pidfd), unix.F_DUPFD_CLOEXEC, 3)
	peer.native.mu.Unlock()
	if err != nil {
		return nil, nil, errors.Join(err, pool.closeFilesV4([]*os.File{socketFile}))
	}
	processFile := os.NewFile(uintptr(process), "actual-purge-request-peer-watch")
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer pool.closeFilesV4([]*os.File{socketFile, processFile})
		defer cancel()
		for ctx.Err() == nil {
			polls := []unix.PollFd{{Fd: int32(socket), Events: unix.POLLRDHUP}, {Fd: int32(process), Events: unix.POLLIN}}
			count, err := unix.Poll(polls, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || count != 0 {
				return
			}
		}
	}()
	return ctx, func() { cancel(); <-done }, nil
}
