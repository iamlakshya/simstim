package egress

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

// memoiseAllow charges each numeric address at most once within a legacy
// callback operation. Server flows reserve their grants through a permit.
func memoiseAllow(allow AllowAddrFunc) AllowAddrFunc {
	if allow == nil {
		return nil
	}
	var mu sync.Mutex
	checked := make(map[netip.AddrPort]bool)
	return func(addr netip.Addr, port int) bool {
		key := netip.AddrPortFrom(addr.Unmap(), uint16(port)) //nolint:gosec // G115: CheckAddr checks the port first
		mu.Lock()
		defer mu.Unlock()
		if verdict, ok := checked[key]; ok {
			return verdict
		}
		verdict := allow(addr, port)
		checked[key] = verdict
		return verdict
	}
}

// DialWithPermit dials for an already authorised flow without memoising its
// checks. A failed candidate releases only that candidate's new reservations.
func (d *Dialer) DialWithPermit(ctx context.Context, eng *Engine, host string, port int, p *Permit) (net.Conn, netip.Addr, string, error) {
	return d.dialWithPermit(ctx, func() *Engine { return eng }, host, port, p, nil)
}

func (d *Dialer) dialWithPermit(ctx context.Context, current func() *Engine, host string, port int, p *Permit, request *Request) (net.Conn, netip.Addr, string, error) {
	if p == nil || p.ledger == nil {
		return nil, netip.Addr{}, "invalid", fmt.Errorf("%w: invalid flow permit", ErrRefusedAddr)
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	precheck := func(addr netip.Addr, prt int) bool {
		return current().evaluateAddrRequest(addr, prt, p, evalPrecheck, request)
	}
	addrs, rule, err := d.ResolveAndCheck(dctx, host, port, precheck)
	if err != nil {
		p.Close()
		return nil, netip.Addr{}, rule, err
	}
	if len(addrs) > 4 {
		addrs = addrs[:4]
	}
	guard := func() error {
		eng := current()
		if request != nil && !eng.revalidateFlow(*request, p).Allow {
			return fmt.Errorf("%w: flow policy changed", ErrRefusedAddr)
		}
		return nil
	}
	reserve := func(addr netip.Addr, prt int) bool {
		return current().evaluateAddrRequest(addr, prt, p, evalReserve, request)
	}
	var lastErr error
	for _, ip := range addrs {
		before := p.checkpoint()
		ap := netip.AddrPortFrom(ip, uint16(port)) //nolint:gosec // G115: ResolveAndCheck checked the port
		conn, err := d.dialLiteralChecked(dctx, ap, reserve, guard)
		if err == nil {
			return conn, ip, "", nil
		}
		p.rollback(before)
		lastErr = err
	}
	p.Close()
	return nil, netip.Addr{}, "dial", fmt.Errorf("dial %s:%d: %w", host, port, lastErr)
}
