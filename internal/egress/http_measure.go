package egress

import (
	"net"
	"sync"
	"sync/atomic"
)

// Origin bytes include TLS and HTTP framing. Closing first stops new I/O,
// unblocks admitted operations and waits for their counts before the audit.
type measuredOriginConn struct {
	net.Conn
	mu       sync.Mutex
	closed   bool
	io       sync.WaitGroup
	once     sync.Once
	closeErr error
	up       atomic.Int64
	down     atomic.Int64
}

func (c *measuredOriginConn) begin() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.io.Add(1)
	return true
}

func (c *measuredOriginConn) Read(p []byte) (int, error) {
	if !c.begin() {
		return 0, net.ErrClosed
	}
	defer c.io.Done()
	n, err := c.Conn.Read(p)
	c.down.Add(int64(n))
	return n, err //nolint:wrapcheck // a net.Conn preserves its underlying read result
}

func (c *measuredOriginConn) Write(p []byte) (int, error) {
	if !c.begin() {
		return 0, net.ErrClosed
	}
	defer c.io.Done()
	n, err := c.Conn.Write(p)
	c.up.Add(int64(n))
	return n, err //nolint:wrapcheck // a net.Conn preserves its underlying write result
}

func (c *measuredOriginConn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.closeErr = c.Conn.Close()
		c.io.Wait()
	})
	return c.closeErr
}
