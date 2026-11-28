package chv

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultVsockRetryInterval is how often DialVsockRetry polls while the
	// guest kernel and guest init boot (isolation spec 20.3).
	DefaultVsockRetryInterval = 50 * time.Millisecond
	// DefaultBootTimeout is the spec 20.3 boot timeout from VMM start to link up.
	DefaultBootTimeout = 60 * time.Second
	maxVsockReplyBytes = 256
)

// DialVsock opens one host-initiated vsock stream through Cloud Hypervisor's
// Unix socket sockPath to guest port (spec 20.3).
//
// It writes "CONNECT <port>\n", reads one "OK <host_port>\n" reply line, and
// returns a net.Conn that transparently replays any bytes already buffered
// after the handshake line. It never creates a "<sockPath>_<port>" listener.
func DialVsock(ctx context.Context, sockPath string, port uint32) (net.Conn, string, error) {
	if port == 0 {
		return nil, "", errors.New("vsock port must be non-zero. Try: pass port 1024")
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return nil, "", fmt.Errorf("dial vsock socket %s: %w", sockPath, err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}

	req := "CONNECT " + strconv.FormatUint(uint64(port), 10) + "\n"
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, "", fmt.Errorf("write vsock CONNECT %d: %w", port, err)
	}

	br := bufio.NewReaderSize(conn, maxVsockReplyBytes)
	line, err := readLineBounded(br, maxVsockReplyBytes)
	if err != nil {
		_ = conn.Close()
		return nil, "", fmt.Errorf("read vsock CONNECT %d reply: %w", port, err)
	}
	reply := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(reply, "OK ") || len(strings.TrimSpace(strings.TrimPrefix(reply, "OK "))) == 0 {
		_ = conn.Close()
		return nil, reply, fmt.Errorf("unexpected vsock handshake reply %q (want \"OK <port>\")", reply)
	}

	// Clear handshake deadline for long-lived link traffic.
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, reply, nil
	}
	return conn, reply, nil
}

// DialVsockRetry dials sockPath for port every interval (default 50 ms) until
// guest init answers "OK <port>", ctx expires, or vmmDone closes (indicating
// the VMM exited before guest init came up).
func DialVsockRetry(ctx context.Context, sockPath string, port uint32, interval time.Duration, vmmDone <-chan struct{}) (net.Conn, string, error) {
	if interval <= 0 {
		interval = DefaultVsockRetryInterval
	}
	var lastErr error
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-vmmDone:
			return nil, "", errors.New("the VMM exited before guest init answered")
		case <-ctx.Done():
			if lastErr != nil {
				return nil, "", fmt.Errorf("guest init did not answer on vsock port %d: %w (%w)", port, ctx.Err(), lastErr)
			}
			return nil, "", fmt.Errorf("guest init did not answer on vsock port %d: %w", port, ctx.Err())
		default:
		}

		attemptCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, reply, err := DialVsock(attemptCtx, sockPath, port)
		cancel()
		if err == nil {
			return conn, reply, nil
		}
		lastErr = err

		select {
		case <-vmmDone:
			return nil, "", errors.New("the VMM exited before guest init answered")
		case <-ctx.Done():
			return nil, "", fmt.Errorf("guest init did not answer on vsock port %d: %w (%w)", port, ctx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func readLineBounded(br *bufio.Reader, limit int) (string, error) {
	var b strings.Builder
	for b.Len() < limit {
		c, err := br.ReadByte()
		if err != nil {
			return "", fmt.Errorf("read byte: %w", err)
		}
		b.WriteByte(c)
		if c == '\n' {
			return b.String(), nil
		}
	}
	return "", fmt.Errorf("vsock reply exceeded %d bytes", limit)
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return n, io.EOF
		}
		return n, fmt.Errorf("read vsock stream: %w", err)
	}
	return n, nil
}
