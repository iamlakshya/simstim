package egress

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

// H1 serialises responses. The server's accepted-byte position at Idle or
// Closed is therefore a fence for that one turn, including final framing and
// trailers. It differs from the copy loop's successful guest-write position.
type httpForwarding struct {
	accepted atomic.Uint64
	mu       sync.Mutex
	written  uint64
	done     bool
	wake     chan struct{}
}

func newHTTPForwarding() *httpForwarding {
	return &httpForwarding{wake: make(chan struct{}, 1)}
}

func (f *httpForwarding) advance(n int, done bool) {
	f.mu.Lock()
	f.written += uint64(n) //nolint:gosec // G115: a net.Conn returns a non-negative byte count
	f.done = f.done || done
	f.mu.Unlock()
	// H1 has one completion waiter. A single coalesced wake avoids an
	// allocation for every response write or SSE event.
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *httpForwarding) wait(ctx context.Context) {
	fence := f.accepted.Load()
	for {
		f.mu.Lock()
		if f.written >= fence || f.done {
			f.mu.Unlock()
			return
		}
		wake := f.wake
		f.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

type httpServerConn struct {
	net.Conn
	forward *httpForwarding
}

func (c *httpServerConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.forward.accepted.Add(uint64(n)) //nolint:gosec // G115: a net.Conn returns a non-negative byte count
	return n, err                     //nolint:wrapcheck // preserve the inherited stream's write result
}

type httpGuestWriter struct {
	net.Conn
	forward *httpForwarding
}

func (c *httpGuestWriter) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.forward.advance(n, err != nil)
	return n, err //nolint:wrapcheck // preserve the inherited stream's write result
}
