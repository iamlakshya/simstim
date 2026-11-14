package egress

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

// Intercepted HTTP streams and spliced flows share the same outbound cap.
// Inbound tunnel slots are separate, so a full set of idle guest tunnels
// cannot deadlock the first request waiting for its origin connection.
func (s *Server) dialOrigin(ctx context.Context, host string, port int, permit *Permit, request *Request) (net.Conn, netip.Addr, string, error) {
	select {
	case s.originSem <- struct{}{}:
	case <-ctx.Done():
		return nil, netip.Addr{}, "dial", ctx.Err()
	}
	conn, addr, rule, err := s.dialer.dialWithPermit(ctx, s.currentEngine, host, port, permit, request)
	if err != nil {
		<-s.originSem
		return nil, addr, rule, err
	}
	return &originSlotConn{Conn: conn, release: func() { <-s.originSem }}, addr, rule, nil
}

type originSlotConn struct {
	net.Conn
	release func()
	once    sync.Once
	err     error
}

func (c *originSlotConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close(); c.release() })
	return c.err
}

func (c *originSlotConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite() //nolint:wrapcheck // preserve a net.Conn half-close result
	}
	return nil
}
