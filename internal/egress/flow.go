package egress

import (
	"context"
	"net"
	"net/netip"
	"sync"
)

type activeFlow struct {
	request       Request
	addr          netip.Addr
	permit        *Permit
	check         func(*Engine, *Dialer) bool
	cancel        func()
	http          bool
	secretPending bool
	secretNames   []string
	done          <-chan struct{}
	carrier       *secretCarrier
}

type flowTracker struct {
	mu       sync.Mutex
	nextID   uint64
	flows    map[uint64]activeFlow
	tunnels  map[*httpTunnel]struct{}
	carriers map[*secretCarrier]struct{}
	closing  bool
	sem      chan struct{}
}

func newFlowTracker(limit int) *flowTracker {
	if limit <= 0 {
		limit = maxActiveFlows
	}
	return &flowTracker{flows: make(map[uint64]activeFlow), tunnels: make(map[*httpTunnel]struct{}), carriers: make(map[*secretCarrier]struct{}), sem: make(chan struct{}, limit)}
}

func (ft *flowTracker) acquire(ctx context.Context) bool {
	select {
	case ft.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (ft *flowTracker) release() { <-ft.sem }

func (ft *flowTracker) add(f activeFlow) uint64 {
	ft.mu.Lock()
	if ft.closing {
		ft.mu.Unlock()
		if f.cancel != nil {
			f.cancel()
		}
		return 0
	}
	ft.nextID++
	id := ft.nextID
	ft.flows[id] = f
	ft.mu.Unlock()
	return id
}

func (ft *flowTracker) remove(id uint64) {
	ft.mu.Lock()
	delete(ft.flows, id)
	ft.mu.Unlock()
}

func (ft *flowTracker) addHTTPRequest(f activeFlow) uint64 {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.closing {
		return 0
	}
	ft.nextID++
	ft.flows[ft.nextID] = f
	return ft.nextID
}

func (ft *flowTracker) snapshot() []activeFlow {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	all := make([]activeFlow, 0, len(ft.flows))
	for _, f := range ft.flows {
		all = append(all, f)
	}
	return all
}

func (ft *flowTracker) revalidate(eng *Engine, dialer *Dialer) {
	for _, f := range ft.snapshot() {
		if !flowAllowed(eng, dialer, f) {
			f.cancel()
		}
	}
}

func flowAllowed(eng *Engine, dialer *Dialer, f activeFlow) bool {
	if f.check != nil {
		return f.check(eng, dialer)
	}
	if f.request.Path == "" && eng.hasPushInspectionHost(f.request.Host, f.request.Port) {
		return false
	}
	if !eng.revalidateFlow(f.request, f.permit).Allow {
		return false
	}
	if f.addr.IsValid() {
		_, ok := dialer.CheckAddr(f.addr, f.request.Port, func(ip netip.Addr, port int) bool {
			return eng.evaluateAddrRequest(ip, port, f.permit, evalRevalidate, &f.request)
		})
		return ok
	}
	return true
}

func (s *Server) flowAllowed(eng *Engine, f activeFlow) bool {
	return flowAllowed(eng, s.dialer, f)
}

func (s *Server) registerFlow(conn, upstream net.Conn, request Request, addr netip.Addr, p *Permit) (uint64, func()) {
	var once sync.Once
	cancel := func() {
		once.Do(func() { _ = conn.Close(); _ = upstream.Close() })
	}
	id := s.flows.add(activeFlow{request: request, addr: addr, permit: p, cancel: cancel})
	return id, cancel
}

// Main passes the exact admitted carrier through verifyAndSplice. An opaque
// upstream cannot outlive the carrier while a new binding starts inspection.
func (s *Server) registerSecretFlow(ctx context.Context, conn, upstream net.Conn, request Request, addr netip.Addr, permit *Permit) (uint64, func()) {
	var once sync.Once
	cancel := func() { once.Do(func() { _ = conn.Close(); _ = upstream.Close() }) }
	carrier, valid := ctx.Value(secretCarrierKey{}).(*secretCarrier)
	if !valid || carrier.tracker != s.flows {
		cancel()
		return 0, cancel
	}
	s.flows.mu.Lock()
	if s.flows.closing || carrier.stopping || carrier.inspected || carrier.ctx.Err() != nil {
		s.flows.mu.Unlock()
		cancel()
		return 0, cancel
	}
	s.flows.nextID++
	id := s.flows.nextID
	s.flows.flows[id] = activeFlow{request: request, addr: addr, permit: permit, cancel: cancel, carrier: carrier, done: carrier.done}
	s.flows.mu.Unlock()
	return id, cancel
}

func (ft *flowTracker) closeAll() {
	ft.mu.Lock()
	ft.closing = true
	ft.mu.Unlock()
	for _, f := range ft.snapshot() {
		f.cancel()
	}
	ft.mu.Lock()
	all := make([]*httpTunnel, 0, len(ft.tunnels))
	for tunnel := range ft.tunnels {
		all = append(all, tunnel)
	}
	carriers := make([]*secretCarrier, 0, len(ft.carriers))
	for carrier := range ft.carriers {
		carriers = append(carriers, carrier)
	}
	ft.mu.Unlock()
	for _, tunnel := range all {
		tunnel.abort()
	}
	for _, carrier := range carriers {
		carrier.abort()
	}
}
