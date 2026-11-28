package egress

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

type refusedRange struct {
	prefix netip.Prefix
	reason string
}

type rangesFile struct {
	Ranges []rangeEntry `toml:"ranges"`
}

type rangeEntry struct {
	CIDR   string `toml:"cidr"`
	Reason string `toml:"reason"`
	Source string `toml:"source"`
}

var (
	loadRangesOnce sync.Once
	defaultRanges  []refusedRange
	errLoadRanges  error

	// Special IPv6 prefixes that embed an IPv4 address (RFC 6052, RFC 3056,
	// RFC 4380, RFC 2765).
	prefixNAT64     = netip.MustParsePrefix("64:ff9b::/96")
	prefix6to4      = netip.MustParsePrefix("2002::/16")
	prefixTeredo    = netip.MustParsePrefix("2001::/32")
	prefixIPv4Trans = netip.MustParsePrefix("::ffff:0:0:0/96")
)

func builtinRanges() ([]refusedRange, error) {
	loadRangesOnce.Do(func() {
		raw, err := rulesFS.ReadFile("rules/ranges.toml")
		if err != nil {
			errLoadRanges = fmt.Errorf("egress: read embedded ranges: %w", err)
			return
		}
		var rf rangesFile
		dec := toml.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rf); err != nil {
			errLoadRanges = fmt.Errorf("egress: decode embedded ranges: %w", err)
			return
		}
		out := make([]refusedRange, 0, len(rf.Ranges))
		for _, r := range rf.Ranges {
			pfx, err := parseCIDR(r.CIDR)
			if err != nil {
				errLoadRanges = fmt.Errorf("egress: embedded range %q: %w", r.CIDR, err)
				return
			}
			out = append(out, refusedRange{prefix: pfx, reason: r.Reason})
		}
		defaultRanges = out
	})
	return defaultRanges, errLoadRanges
}

// parseCIDR parses standard CIDRs as well as short IPv4 CIDRs such as "10/8",
// "172.16/12", "192.168/16" and "100.64/10".
func parseCIDR(raw string) (netip.Prefix, error) {
	if pfx, err := netip.ParsePrefix(raw); err == nil {
		return pfx.Masked(), nil
	}
	parts := strings.SplitN(raw, "/", 2)
	if len(parts) != 2 || strings.ContainsRune(parts[0], ':') {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR %q. Try: use address/prefix syntax", raw)
	}
	octets := strings.Split(parts[0], ".")
	if len(octets) == 0 || len(octets) > 4 {
		return netip.Prefix{}, fmt.Errorf("invalid IPv4 CIDR %q. Try: use address/prefix syntax", raw)
	}
	var b [4]byte
	for i, o := range octets {
		v, err := strconv.Atoi(o)
		if err != nil || v < 0 || v > 255 {
			return netip.Prefix{}, fmt.Errorf("invalid IPv4 CIDR %q. Try: use valid octets", raw)
		}
		b[i] = byte(v)
	}
	bits, err := strconv.Atoi(parts[1])
	if err != nil || bits < 0 || bits > 32 {
		return netip.Prefix{}, fmt.Errorf("invalid IPv4 prefix length in %q. Try: use 0 to 32", raw)
	}
	return netip.PrefixFrom(netip.AddrFrom4(b), bits).Masked(), nil
}

// AddressPolicy decides whether a resolved IP address may be dialled.
// It refuses all private, loopback, link-local, metadata, CGNAT, transition
// and host-local addresses unless explicitly granted by IP or CIDR.
type AddressPolicy struct {
	ranges    []refusedRange
	hostAddrs map[netip.Addr]struct{}
}

// NewAddressPolicy loads the built-in refused ranges and captures the host's
// own interface addresses at start.
func NewAddressPolicy(extraHostAddrs ...netip.Addr) (*AddressPolicy, error) {
	ranges, err := builtinRanges()
	if err != nil {
		return nil, err
	}
	hostAddrs := collectHostAddrs()
	for _, a := range extraHostAddrs {
		if a.IsValid() {
			hostAddrs[a.WithZone("").Unmap()] = struct{}{}
		}
	}
	return &AddressPolicy{ranges: ranges, hostAddrs: hostAddrs}, nil
}

// NewAddressPolicyWithHosts builds an AddressPolicy with an explicit host
// address set, for deterministic tests.
func NewAddressPolicyWithHosts(hostAddrs []netip.Addr) (*AddressPolicy, error) {
	ranges, err := builtinRanges()
	if err != nil {
		return nil, err
	}
	m := make(map[netip.Addr]struct{}, len(hostAddrs))
	for _, a := range hostAddrs {
		if a.IsValid() {
			m[a.WithZone("").Unmap()] = struct{}{}
		}
	}
	return &AddressPolicy{ranges: ranges, hostAddrs: m}, nil
}

func collectHostAddrs() map[netip.Addr]struct{} {
	out := map[netip.Addr]struct{}{}
	ifAddrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range ifAddrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if addr, ok := netip.AddrFromSlice(ip); ok {
			out[addr.WithZone("").Unmap()] = struct{}{}
		}
	}
	return out
}

// RefusedAddrError indicates an address fell into a refused range or belonged
// to the host itself.
type RefusedAddrError struct {
	Addr   netip.Addr
	Reason string
}

func (e *RefusedAddrError) Error() string {
	return fmt.Sprintf("egress: address %s refused (%s). Try: allow the address explicitly if it is intended", e.Addr, e.Reason)
}

// CheckAddr checks whether addr is safe to dial. If allowExplicit returns true
// for the normalized address, the check passes.
func (p *AddressPolicy) CheckAddr(addr netip.Addr, allowExplicit func(netip.Addr) bool) error {
	if !addr.IsValid() {
		return &RefusedAddrError{Addr: addr, Reason: "invalid address"}
	}
	clean := addr.WithZone("").Unmap()
	if allowExplicit != nil && allowExplicit(clean) {
		return nil
	}
	if _, own := p.hostAddrs[clean]; own {
		return &RefusedAddrError{Addr: clean, Reason: "host's own address"}
	}
	if clean.Is6() {
		if err := p.checkEmbeddedIPv4(clean, allowExplicit); err != nil {
			return err
		}
	}
	for _, r := range p.ranges {
		if r.prefix.Contains(clean) {
			return &RefusedAddrError{Addr: clean, Reason: r.reason}
		}
	}
	return nil
}

// checkEmbeddedIPv4 unpacks IPv4 addresses carried inside IPv6 transition
// formats (NAT64, 6to4, Teredo and IPv4-translated) and checks each against
// the IPv4 refused ranges and host addresses.
func (p *AddressPolicy) checkEmbeddedIPv4(addr netip.Addr, allowExplicit func(netip.Addr) bool) error {
	b := addr.As16()
	switch {
	case prefixNAT64.Contains(addr):
		inner := netip.AddrFrom4([4]byte(b[12:16]))
		if err := p.CheckAddr(inner, allowExplicit); err != nil {
			return &RefusedAddrError{Addr: addr, Reason: "NAT64 embeds refused address " + inner.String()}
		}
	case prefixIPv4Trans.Contains(addr):
		inner := netip.AddrFrom4([4]byte(b[12:16]))
		if err := p.CheckAddr(inner, allowExplicit); err != nil {
			return &RefusedAddrError{Addr: addr, Reason: "IPv4-translated embeds refused address " + inner.String()}
		}
		return &RefusedAddrError{Addr: addr, Reason: "IPv4-translated prefix (RFC 2765)"}
	case prefix6to4.Contains(addr):
		inner := netip.AddrFrom4([4]byte(b[2:6]))
		if err := p.CheckAddr(inner, allowExplicit); err != nil {
			return &RefusedAddrError{Addr: addr, Reason: "6to4 embeds refused address " + inner.String()}
		}
	case prefixTeredo.Contains(addr):
		server := netip.AddrFrom4([4]byte(b[4:8]))
		if err := p.CheckAddr(server, allowExplicit); err != nil {
			return &RefusedAddrError{Addr: addr, Reason: "Teredo server embeds refused address " + server.String()}
		}
		client := netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]})
		if err := p.CheckAddr(client, allowExplicit); err != nil {
			return &RefusedAddrError{Addr: addr, Reason: "Teredo client embeds refused address " + client.String()}
		}
	}
	return nil
}
