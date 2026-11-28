package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// Resolver looks up IP addresses for a host name.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc opens a network connection to an explicit IP:port address.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// SafeDialer resolves host names once, validates every returned IP against
// AddressPolicy, and dials only the checked IP addresses. A second DNS lookup
// never happens on the dial path, stopping DNS rebinding attacks.
type SafeDialer struct {
	Addrs    *AddressPolicy
	Engine   *Engine
	Resolver Resolver
	Dial     DialFunc
}

// NewSafeDialer returns a SafeDialer backed by the system resolver and a TCP
// dialer with sensible timeouts.
func NewSafeDialer(addrs *AddressPolicy, engine *Engine) *SafeDialer {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	return &SafeDialer{
		Addrs:    addrs,
		Engine:   engine,
		Resolver: net.DefaultResolver,
		Dial:     d.DialContext,
	}
}

type checkedAddrsKey struct{}

// withCheckedAddrs attaches pre-verified IP addresses to ctx so that an
// http.Transport DialContext reuses the exact addresses already checked during
// request filtering without running a second lookup.
func withCheckedAddrs(ctx context.Context, addrs []netip.Addr) context.Context {
	return context.WithValue(ctx, checkedAddrsKey{}, addrs)
}

func checkedAddrsFrom(ctx context.Context) ([]netip.Addr, bool) {
	addrs, ok := ctx.Value(checkedAddrsKey{}).([]netip.Addr)
	return addrs, ok && len(addrs) > 0
}

// ResolveAndCheck resolves host once and verifies every returned IP against
// the address policy. If any address is refused, the whole lookup fails closed
// with *RefusedAddrError.
func (d *SafeDialer) ResolveAndCheck(ctx context.Context, host string, port int) ([]netip.Addr, error) {
	allowIP := func(a netip.Addr) bool {
		return d.Engine != nil && d.Engine.AllowExplicitIP(a, port)
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		clean := addr.WithZone("").Unmap()
		if err := d.Addrs.CheckAddr(clean, allowIP); err != nil {
			return nil, err
		}
		return []netip.Addr{clean}, nil
	}
	res := d.Resolver
	if res == nil {
		res = net.DefaultResolver
	}
	ips, err := res.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolve %q: %w. Try: check the host name and network connectivity", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("egress: resolve %q returned no addresses. Try: check DNS for this host", host)
	}
	checked := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		clean := ip.WithZone("").Unmap()
		if err := d.Addrs.CheckAddr(clean, allowIP); err != nil {
			return nil, err
		}
		checked = append(checked, clean)
	}
	return checked, nil
}

// DialChecked dials the first reachable address in addrs on port, using only
// the already-verified IP addresses.
func (d *SafeDialer) DialChecked(ctx context.Context, addrs []netip.Addr, port int) (net.Conn, netip.Addr, error) {
	if len(addrs) == 0 {
		return nil, netip.Addr{}, errors.New("egress: no verified addresses to dial. Try: check the host name")
	}
	if port < 1 || port > 65535 {
		return nil, netip.Addr{}, fmt.Errorf("egress: invalid port %d. Try: use a port between 1 and 65535", port)
	}
	dialFn := d.Dial
	if dialFn == nil {
		nd := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		dialFn = nd.DialContext
	}
	var lastErr error
	for _, a := range addrs {
		ap := netip.AddrPortFrom(a, uint16(port))
		conn, err := dialFn(ctx, "tcp", ap.String())
		if err == nil {
			return conn, a, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, netip.Addr{}, fmt.Errorf("egress: dial port %d: %w. Try: verify the destination is reachable", port, lastErr)
}

// DialContext implements the http.Transport DialContext signature. If ctx
// already carries pre-checked addresses (from request filtering), it dials them
// directly without resolving again; otherwise it resolves once and checks
// every address before dialing.
func (d *SafeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("egress: unsupported network %q. Try: use TCP", network)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("egress: invalid dial address %q: %w. Try: use host:port", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("egress: invalid dial port %q. Try: use a port between 1 and 65535", portStr)
	}
	addrs, ok := checkedAddrsFrom(ctx)
	if !ok {
		addrs, err = d.ResolveAndCheck(ctx, host, port)
		if err != nil {
			return nil, err
		}
	}
	conn, _, err := d.DialChecked(ctx, addrs, port)
	return conn, err
}
