package microvm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// BootTimeout bounds VMM start through the guest init's hello.
const BootTimeout = 60 * time.Second

// GuestPort is the guest init's vsock port. Trusted code never listens on it.
const GuestPort uint32 = 1024

// ConnectGuest dials only the VMM's private Unix endpoint. It writes the
// Cloud Hypervisor host-to-guest command and preserves prefetched link bytes.
// No guest-to-host socket or named listener is ever created.
func ConnectGuest(ctx context.Context, socket string) (net.Conn, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || strings.ContainsRune(socket, 0) {
		return nil, fmt.Errorf("the VMM vsock endpoint is not an absolute clean path")
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, BootTimeout)
		defer cancel()
	}
	return connectGuest(ctx, socket, func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, func(ctx context.Context) error {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	})
}

func connectGuest(ctx context.Context, socket string, dial func(context.Context, string) (net.Conn, error), pause func(context.Context) error) (net.Conn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		conn, err := dial(ctx, socket)
		if err == nil {
			reader, err := connectVsock(ctx, conn)
			if err == nil {
				return &bufferedVsock{Conn: conn, reader: reader}, nil
			}
			conn.Close()
		}
		if err := pause(ctx); err != nil {
			return nil, err
		}
	}
}

func connectVsock(ctx context.Context, conn net.Conn) (*bufio.Reader, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(BootTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set VMM link deadline: %w", err)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = conn.SetDeadline(time.Time{})
	}()
	command := fmt.Sprintf("CONNECT %d\n", GuestPort)
	if n, err := conn.Write([]byte(command)); err != nil {
		return nil, fmt.Errorf("connect guest vsock listener: %w", err)
	} else if n != len(command) {
		return nil, fmt.Errorf("connect guest vsock listener: %w", io.ErrShortWrite)
	}
	reader := bufio.NewReaderSize(conn, 128)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return nil, fmt.Errorf("read VMM vsock reply: %w", err)
	}
	fields := strings.Fields(string(line))
	if len(fields) != 2 || fields[0] != "OK" {
		return nil, fmt.Errorf("the VMM has not accepted the guest connection")
	}
	// The acknowledgement names the allocated host-side port, not 1024.
	port, err := strconv.ParseUint(fields[1], 10, 32)
	if err != nil || port == 0 {
		return nil, fmt.Errorf("the VMM returned an invalid host-side vsock port")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return reader, nil
}

type bufferedVsock struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedVsock) Read(p []byte) (int, error) { return c.reader.Read(p) }
