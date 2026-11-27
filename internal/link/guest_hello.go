package link

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ReadGuestHello checks the actual connection's guest hello with the one
// canonical link decoder before VM startup returns. It returns the SAME
// connection with its exact already-read bytes replayed to New, including any
// buffered following bytes. It grants no VM Need, native-owner or ABI authority.
// The caller owns Close on every outcome and is the only reader until return.
func ReadGuestHello(ctx context.Context, conn net.Conn) (result net.Conn, failure error) {
	if ctx == nil || conn == nil {
		return nil, errors.New("link: guest hello has no owned connection or lifetime. Try: use the actual VM bootstrap connection")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(DefaultHelloTimeout)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("bound the guest hello read: %w", err)
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		_ = conn.SetReadDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-cancelDone
		}
		reset := conn.SetReadDeadline(time.Time{})
		if err := ctx.Err(); err != nil {
			failure = errors.Join(err, failure, reset)
		} else {
			failure = errors.Join(failure, reset)
		}
		if failure != nil {
			result = nil
		}
	}()
	var prefix bytes.Buffer
	// A small buffer preserves the canonical maxFrame/helloMax checks while
	// bounding accepted replay to one hello and at most fifteen following
	// bytes. No second decoder or independent handshake schema is added.
	reader := bufio.NewReaderSize(io.TeeReader(conn, &prefix), 16)
	check := newSession(conn, Supervisor, Options{})
	checked := check.readHello(reader)
	if checked != nil {
		return nil, checked
	}
	if prefix.Len() > helloMax+32 {
		return nil, breach("the checked hello exceeded its replay bound")
	}
	return &guestHelloConn{Conn: conn, prefix: prefix.Bytes()}, nil
}

// guestHelloConn forwards the original byte stream. Wrappers between the VM
// resource and New cannot lose an out-of-band adopted-hello flag: the ordinary
// New reader validates the same exact bytes through the same decoder again.
type guestHelloConn struct {
	net.Conn
	mu     sync.Mutex
	prefix []byte
}

func (conn *guestHelloConn) Read(data []byte) (int, error) {
	conn.mu.Lock()
	if len(conn.prefix) > 0 {
		n := copy(data, conn.prefix)
		conn.prefix = conn.prefix[n:]
		if len(conn.prefix) == 0 {
			conn.prefix = nil
		}
		conn.mu.Unlock()
		return n, nil
	}
	conn.mu.Unlock()
	return conn.Conn.Read(data)
}
