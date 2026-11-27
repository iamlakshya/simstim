package egress

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// One original origin connection owns this accounting view. It observes only
// successful upstream TCP writes, including HTTP/TLS framing when present.
// Model APIs and an actually inspected granted push use their existing guards.
type uploadBudgetConn struct {
	net.Conn
	server  *Server
	request Request
	permit  *Permit
	addr    netip.Addr
	scope   *originRequest
	ctx     context.Context
	cancel  context.CancelFunc
	writeMu sync.Mutex
	once    sync.Once
	err     error
}

func (*uploadBudgetConn) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (s *Server) budgetOriginConn(ctx context.Context, conn net.Conn, request *Request, permit *Permit, addr netip.Addr) net.Conn {
	if request == nil || s.uploads == nil {
		return conn
	}
	var scope *originRequest
	if candidate, ok := ctx.Value(originRequestKey{}).(*originRequest); ok && candidate.s == s && candidate.permit == permit {
		scope, ctx = candidate, candidate.ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	return &uploadBudgetConn{Conn: conn, server: s, request: *request, permit: permit, addr: addr, scope: scope, ctx: ctx, cancel: cancel}
}

func (c *uploadBudgetConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close(); c.cancel() })
	return c.err
}

func (c *uploadBudgetConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite() //nolint:wrapcheck // retain the original half-close
	}
	return nil
}

func (c *uploadBudgetConn) exempt() bool {
	if kind, _, err := httpClass(c.request.Host); err == nil && kind == "model" {
		return true
	}
	if c.scope != nil {
		c.scope.mu.Lock()
		service := c.scope.service
		c.scope.mu.Unlock()
		return service != nil && service.pushRepo != "" && service.allowedAtState(c.server.currentEngine(), c.permit, c.server.currentMembers())
	}
	return false
}

func (c *uploadBudgetConn) allowance(length int) (uploadReservation, int, error) {
	s := c.server
	if err := s.policyMu.LockContext(c.ctx); err != nil {
		return uploadReservation{}, 0, err
	}
	defer s.policyMu.Unlock()
	state, engine := s.currentMembers(), s.currentEngine()
	if state == nil || !s.flowAllowed(engine, activeFlow{request: c.request, addr: c.addr, permit: c.permit}) {
		return uploadReservation{}, 0, errHoldEnded
	}
	if c.exempt() {
		return uploadReservation{}, length, nil
	}
	limit, err := uploadLimit(state.metadata.Policy)
	if err != nil {
		return uploadReservation{}, 0, err
	}
	candidate := c.request
	candidate.uploadBudgetOnly = true
	precheck := engine.evaluate(candidate, c.permit, evalBudgetPrecheck)
	if precheck.Allow && precheck.publicOnly {
		if _, allowed := s.dialer.CheckAddr(c.addr, c.request.Port, nil); !allowed {
			return uploadReservation{}, 0, errHoldEnded
		}
	}
	override := precheck.Allow && engine.evaluate(candidate, c.permit, evalBudgetReserve).Allow
	return s.uploads.reserve(c.request.Host, length, limit, s.now(), override)
}

func (c *uploadBudgetConn) Write(payload []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	written := 0
	for written < len(payload) {
		reservation, allowed, err := c.allowance(len(payload) - written)
		if err != nil {
			_ = c.Close()
			return written, err
		}
		if allowed == 0 {
			if err := c.awaitBudget(); err != nil {
				_ = c.Close()
				return written, err
			}
			continue
		}
		n, err := c.Conn.Write(payload[written : written+allowed])
		if reservation.window != nil {
			c.server.uploads.finish(reservation, n)
		}
		written += n
		if err != nil {
			return written, err //nolint:wrapcheck // retain the original physical write result
		}
		if n != allowed {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
