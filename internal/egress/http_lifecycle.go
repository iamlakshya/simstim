package egress

import (
	"context"
	"net"
	"sync"
	"time"
)

type httpTunnelKey struct{}

// A tunnel owns no grant. Its requests each carry their own immutable permit.
// The request semaphore bounds both this set and deferred finalisers globally.
type httpTunnel struct {
	ctx      context.Context
	cancel   context.CancelFunc
	conn     net.Conn
	mu       sync.Mutex
	requests map[*originRequest]struct{}
	bodyStop func()
	stopping bool
	once     sync.Once
}

func (s *Server) httpTunnel(ctx context.Context, conn net.Conn) (context.Context, *httpTunnel, func()) {
	if tunnel, ok := ctx.Value(httpTunnelKey{}).(*httpTunnel); ok {
		return ctx, tunnel, func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	tunnel := &httpTunnel{ctx: ctx, cancel: cancel, conn: conn, requests: make(map[*originRequest]struct{})}
	// Guest TLS terminates locally. It is a bounded carrier, not an origin
	// grant. Policy reloads recheck every request, while session shutdown
	// owns this carrier's close, including its final response framing.
	s.flows.mu.Lock()
	if s.flows.closing {
		cancel()
	} else {
		s.flows.tunnels[tunnel] = struct{}{}
	}
	s.flows.mu.Unlock()
	if ctx.Err() != nil {
		tunnel.abort()
	}
	return context.WithValue(ctx, httpTunnelKey{}, tunnel), tunnel, func() {
		tunnel.mu.Lock()
		tunnel.stopping = true
		pending := make([]*originRequest, 0, len(tunnel.requests))
		for request := range tunnel.requests {
			pending = append(pending, request)
		}
		tunnel.mu.Unlock()
		cancel()
		for _, request := range pending {
			request.abort()
		}
		s.flows.mu.Lock()
		delete(s.flows.tunnels, tunnel)
		s.flows.mu.Unlock()
	}
}

func (t *httpTunnel) add(request *originRequest) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopping || t.ctx.Err() != nil {
		return false
	}
	t.requests[request] = struct{}{}
	return true
}

func (t *httpTunnel) remove(request *originRequest) {
	t.mu.Lock()
	delete(t.requests, request)
	t.mu.Unlock()
}

func (t *httpTunnel) abort() {
	t.once.Do(func() {
		t.cancel()
		t.mu.Lock()
		stopBody := t.bodyStop
		t.mu.Unlock()
		if stopBody != nil {
			stopBody()
		}
		// An abort discards pending bytes. Ordinary completion uses the
		// forwarding fence and graceful close instead of these deadlines.
		_ = t.conn.SetDeadline(time.Now())
		closeHTTPConnection(t.conn)
	})
}

func (t *httpTunnel) setBodyStop(stop func()) {
	t.mu.Lock()
	t.bodyStop = stop
	stopped := t.ctx.Err() != nil
	t.mu.Unlock()
	if stopped {
		stop()
	}
}

type httpAuditResult struct {
	mu    sync.Mutex
	entry AuditEntry
}

func (r *httpAuditResult) set(entry AuditEntry) {
	r.mu.Lock()
	r.entry = entry
	r.mu.Unlock()
}

func (r *httpAuditResult) get() AuditEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entry
}
