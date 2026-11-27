package egress

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/nettarget"
)

//go:embed rules/ranges.toml
var rangesTOML string

type rangesFile struct {
	Refused []rangeEntry `toml:"refused"`
}

type rangeEntry struct {
	CIDR   string `toml:"cidr"`
	Note   string `toml:"note"`
	Source string `toml:"source"`
}

type parsedRange struct {
	prefix netip.Prefix
	cidr   string
	source string
}

var loadRefusedRanges = sync.OnceValues(func() ([]parsedRange, error) {
	var f rangesFile
	md, err := toml.Decode(rangesTOML, &f)
	if err != nil {
		return nil, fmt.Errorf("decode ranges.toml: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("unknown keys in ranges.toml: %v", u)
	}
	if len(f.Refused) == 0 {
		return nil, errors.New("ranges.toml has no refused ranges")
	}
	out := make([]parsedRange, 0, len(f.Refused))
	for _, e := range f.Refused {
		if !strings.HasPrefix(e.Source, "https://") {
			return nil, fmt.Errorf("range %q lacks an https source link", e.CIDR)
		}
		p, err := ParseRange(e.CIDR)
		if err != nil {
			return nil, fmt.Errorf("range %q: %w", e.CIDR, err)
		}
		out = append(out, parsedRange{prefix: p, cidr: e.CIDR, source: e.Source})
	}
	return out, nil
})

var nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")

// ParseRange parses a CIDR range, a single IP address, or a short IPv4 CIDR
// form such as "10/8" or "172.16/12" (spec 7.2).
func ParseRange(raw string) (netip.Prefix, error) {
	prefix, err := nettarget.ParseRange(raw)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse network range: %w", err)
	}
	return prefix, nil
}

// HostInterfaceAddrs returns the host's own interface addresses, stripped of
// IPv6 zones and unmapped when IPv4-in-IPv6.
func HostInterfaceAddrs() ([]netip.Addr, error) {
	raw, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("read host interface addresses: %w", err)
	}
	var out []netip.Addr
	for _, a := range raw {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, addr.WithZone("").Unmap())
		}
	}
	return out, nil
}

// embeddedIPv4s returns any IPv4 addresses carried inside an IPv6 transition
// or translation address: IPv4-mapped (::ffff:0:0/96), IPv4-compatible
// (::/96), SIIT (::ffff:0:0:0/96), NAT64 (64:ff9b::/96 and 64:ff9b:1::/48),
// 6to4 (2002::/16) and Teredo (2001::/32).
func embeddedIPv4s(addr netip.Addr) []netip.Addr {
	if !addr.Is6() {
		return nil
	}
	if addr.Is4In6() {
		return []netip.Addr{addr.Unmap()}
	}
	b := addr.As16()
	var out []netip.Addr
	switch {
	case isZero(b[:12]) && (!isZero(b[12:15]) || b[15] > 1):
		// IPv4-compatible ::w.x.y.z (:: and ::1 are already refused as IPv6).
		out = append(out, netip.AddrFrom4([4]byte(b[12:16])))
	case isZero(b[:8]) && b[8] == 0xff && b[9] == 0xff && b[10] == 0 && b[11] == 0:
		// SIIT ::ffff:0:w.x.y.z (RFC 2765).
		out = append(out, netip.AddrFrom4([4]byte(b[12:16])))
	case nat64WellKnown.Contains(addr):
		// Well-known NAT64 64:ff9b::/96 (RFC 6052).
		out = append(out, netip.AddrFrom4([4]byte(b[12:16])))
	case b[0] == 0x00 && b[1] == 0x64 && b[2] == 0xff && b[3] == 0x9b && b[4] == 0x00 && b[5] == 0x01:
		// Local-use NAT64 64:ff9b:1::/48 (RFC 8215, /48 and /96 positions).
		out = append(out, netip.AddrFrom4([4]byte{b[6], b[7], b[9], b[10]}), netip.AddrFrom4([4]byte(b[12:16])))
	case b[0] == 0x20 && b[1] == 0x02:
		// 6to4 2002::/16 (RFC 3056).
		out = append(out, netip.AddrFrom4([4]byte(b[2:6])))
	case b[0] == 0x20 && b[1] == 0x01 && b[2] == 0x00 && b[3] == 0x00:
		// Teredo 2001::/32 (RFC 4380): server IPv4 and XOR-obfuscated client IPv4.
		out = append(out,
			netip.AddrFrom4([4]byte(b[4:8])),
			netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]}),
		)
	}
	return out
}

func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// AllowAddrFunc reports whether a numeric IP or CIDR allow rule explicitly
// opens addr on port.
type AllowAddrFunc func(addr netip.Addr, port int) bool

// Dialer resolves names once, checks every resolved address against
// ranges.toml and the host's own addresses, and dials only verified literal
// IP addresses so a second DNS lookup cannot rebind to a refused target.
type Dialer struct {
	// Lookup resolves a hostname to IP addresses. Nil uses net.DefaultResolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// DialTCP dials a verified literal AddrPort. Nil uses net.Dialer with a
	// Control check on the kernel socket address.
	DialTCP func(ctx context.Context, ap netip.AddrPort) (net.Conn, error)
	// HostAddrs are the host's own interface addresses, captured at start.
	HostAddrs []netip.Addr
	// Timeout bounds DNS resolution and TCP connection setup.
	Timeout time.Duration

	// resolver overrides net.DefaultResolver in tests when Lookup is nil.
	resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
}

// ErrRefusedAddr marks an address blocked by ranges.toml or HostAddrs.
var ErrRefusedAddr = errors.New("refused destination address")

// CheckAddr reports whether addr on port is permitted. When refused, rule
// names the matching range or host address rule, such as "range:169.254.0.0/16"
// or "range:host".
func (d *Dialer) CheckAddr(addr netip.Addr, port int, allow AllowAddrFunc) (rule string, ok bool) {
	if !addr.IsValid() || addr.Zone() != "" || port < 1 || port > 65535 {
		return "range:invalid", false
	}
	allow = memoiseAllow(allow)
	for _, inner := range embeddedIPv4s(addr) {
		if r := d.refusedReason(inner); r != "" {
			if allow == nil || !allow(inner, port) {
				return r, false
			}
		}
	}
	if r := d.refusedReason(addr); r != "" {
		if allow != nil && (allow(addr, port) || (addr.Is4In6() && allow(addr.Unmap(), port))) {
			return "", true
		}
		return r, false
	}
	return "", true
}

func (d *Dialer) refusedReason(addr netip.Addr) string {
	ranges, err := loadRefusedRanges()
	if err != nil {
		return "range:invalid"
	}
	u := addr.Unmap()
	for _, h := range d.HostAddrs {
		if h.Unmap() == u {
			return "range:host"
		}
	}
	for _, r := range ranges {
		if r.prefix.Contains(addr) || (addr != u && r.prefix.Contains(u)) {
			return "range:" + r.cidr
		}
	}
	if u.IsLoopback() || u.IsLinkLocalUnicast() || u.IsLinkLocalMulticast() || u.IsMulticast() || u.IsUnspecified() || u.IsPrivate() {
		return "range:builtin"
	}
	return ""
}

// ResolveAndCheck resolves host once and verifies every returned address with
// CheckAddr. If any resolved address is refused, the whole target is refused.
func (d *Dialer) ResolveAndCheck(ctx context.Context, host string, port int, allow AllowAddrFunc) ([]netip.Addr, string, error) {
	allow = memoiseAllow(allow)
	if addr, err := netip.ParseAddr(host); err == nil {
		if rule, ok := d.CheckAddr(addr, port, allow); !ok {
			return nil, rule, fmt.Errorf("%w: %s (%s)", ErrRefusedAddr, addr, rule)
		}
		return []netip.Addr{addr}, "", nil
	}
	var (
		addrs []netip.Addr
		err   error
	)
	if d.Lookup != nil {
		addrs, err = d.Lookup(ctx, host)
	} else {
		r := d.resolver
		if r == nil {
			r = net.DefaultResolver
		}
		addrs, err = r.LookupNetIP(ctx, "ip", strings.TrimSuffix(host, ".")+".")
	}
	if err != nil {
		return nil, "dns", fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, "dns", fmt.Errorf("resolve %s: no addresses", host)
	}
	verified := make([]netip.Addr, 0, len(addrs))
	for _, raw := range addrs {
		ip := raw.WithZone("")
		if rule, ok := d.CheckAddr(ip, port, allow); !ok {
			return nil, rule, fmt.Errorf("%w: %s resolved to %s (%s)", ErrRefusedAddr, host, ip, rule)
		}
		verified = append(verified, ip)
	}
	return verified, "", nil
}

// Dial resolves host once, checks every address, and dials only a verified
// literal IP:port. It never passes a hostname to net.Dialer.
func (d *Dialer) Dial(ctx context.Context, host string, port int, allow AllowAddrFunc) (net.Conn, netip.Addr, string, error) {
	allow = memoiseAllow(allow)
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addrs, rule, err := d.ResolveAndCheck(dctx, host, port, allow)
	if err != nil {
		return nil, netip.Addr{}, rule, err
	}
	if len(addrs) > 4 {
		addrs = addrs[:4]
	}
	var lastErr error
	for _, ip := range addrs {
		ap := netip.AddrPortFrom(ip, uint16(port)) //nolint:gosec // G115: ResolveAndCheck checked 1 <= port <= 65535
		conn, err := d.dialLiteral(dctx, ap, allow)
		if err == nil {
			return conn, ip, "", nil
		}
		lastErr = err
	}
	return nil, netip.Addr{}, "dial", fmt.Errorf("dial %s:%d: %w", host, port, lastErr)
}

func (d *Dialer) dialLiteral(ctx context.Context, ap netip.AddrPort, allow AllowAddrFunc) (net.Conn, error) {
	return d.dialLiteralChecked(ctx, ap, allow, nil)
}

func (d *Dialer) dialLiteralChecked(ctx context.Context, ap netip.AddrPort, allow AllowAddrFunc, guard func() error) (net.Conn, error) {
	if guard != nil {
		if err := guard(); err != nil {
			return nil, err
		}
	}
	if rule, ok := d.CheckAddr(ap.Addr(), int(ap.Port()), allow); !ok {
		return nil, fmt.Errorf("%w: %s (%s)", ErrRefusedAddr, ap.Addr(), rule)
	}
	if d.DialTCP != nil {
		return d.DialTCP(ctx, ap)
	}
	nd := net.Dialer{
		Control: func(network, address string, _ syscall.RawConn) error {
			if guard != nil {
				if err := guard(); err != nil {
					return err
				}
			}
			switch network {
			case "tcp", "tcp4", "tcp6":
			default:
				return fmt.Errorf("%w: unexpected network %s", ErrRefusedAddr, network)
			}
			got, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: non-literal dial target %q", ErrRefusedAddr, address)
			}
			if rule, ok := d.CheckAddr(got.Addr(), int(got.Port()), allow); !ok {
				return fmt.Errorf("%w: %s (%s)", ErrRefusedAddr, got.Addr(), rule)
			}
			return nil
		},
	}
	conn, err := nd.DialContext(ctx, "tcp", ap.String())
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", ap, err)
	}
	return conn, nil
}
