//go:build linux

package process

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func (s *sandbox) adoptInit(conn *net.UnixConn) error {
	data := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	n, oob, flags, _, err := conn.ReadMsgUnix(data, control)
	if err != nil {
		return fmt.Errorf("receive namespace-init ownership: %w", err)
	}
	msgs, err := unix.ParseSocketControlMessage(control[:oob])
	if err != nil {
		return fmt.Errorf("parse namespace-init ownership: %w", err)
	}
	var received []int
	for _, m := range msgs {
		fds, e := unix.ParseUnixRights(&m)
		if e != nil {
			for _, fd := range received {
				unix.Close(fd)
			}
			return fmt.Errorf("parse namespace-init descriptor: %w", e)
		}
		received = append(received, fds...)
	}
	if n != 1 || data[0] != 'P' || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0 || len(received) != 1 {
		for _, fd := range received {
			unix.Close(fd)
		}
		return fmt.Errorf("invalid namespace-init ownership message")
	}
	fd := received[0]
	unix.CloseOnExec(fd)
	// Verify this is a pidfd without delivering a signal. The launcher cannot
	// substitute a file, mount descriptor or arbitrary open socket.
	if err = signalInit(fd, 0, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		unix.Close(fd)
		return fmt.Errorf("validate namespace-init pidfd: %w", err)
	}
	s.mu.Lock()
	s.pidfd = fd
	// Ownership is published before the acknowledgement. Kill and reap use
	// this same lock, so neither can release or overlook an acknowledged init.
	_, err = conn.Write([]byte{'A'})
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("acknowledge namespace-init ownership: %w", err)
	}
	return nil
}
