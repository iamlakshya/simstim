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
	accepted   atomic.Uint64
	mu         sync.Mutex
	written    uint64
	done       bool
	failed     bool
	body       *physicalHTTP1Body
	bodyStart  uint64
	wake       chan struct{}
	usage      *modelWebSocketUsage
	usageStart uint64
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
	if n > 0 {
		c.forward.observeSuccessfulWrite(p[:n])
	}
	if err != nil || n != len(p) {
		c.forward.mu.Lock()
		c.forward.failed = true
		c.forward.mu.Unlock()
	}
	c.forward.advance(n, err != nil)
	return n, err //nolint:wrapcheck // preserve the inherited stream's write result
}

func (f *httpForwarding) observeModelWebSocket(usage *modelWebSocketUsage) {
	f.mu.Lock()
	f.usage, f.usageStart = usage, f.accepted.Load()
	f.mu.Unlock()
}
func (f *httpForwarding) observeSuccessfulWrite(payload []byte) {
	f.mu.Lock()
	usage, start, written := f.usage, f.usageStart, f.written
	if f.body != nil && written+uint64(len(payload)) > f.bodyStart {
		bodyPayload := payload
		if written < f.bodyStart {
			bodyPayload = payload[f.bodyStart-written:]
		}
		if f.body.feed(bodyPayload) != nil {
			f.failed = true
		}
	}
	f.mu.Unlock()
	if usage == nil || written+uint64(len(payload)) <= start {
		return
	}
	if written < start {
		payload = payload[start-written:]
	}
	usage.observe(payload)
}

func (f *httpForwarding) observeModelHTTP(turn *ModelUsageTurnV1, method string) {
	f.mu.Lock()
	f.body = &physicalHTTP1Body{method: method, observe: turn.ForwardedV1}
	f.bodyStart = f.accepted.Load()
	f.mu.Unlock()
}

func (f *httpForwarding) physicalComplete() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.failed && f.written >= f.accepted.Load()
}
