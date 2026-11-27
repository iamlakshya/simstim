package nettarget

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// ParseRange parses a CIDR range, a single IP address, or a short IPv4 CIDR
// form such as "10/8" or "172.16/12" (spec 7.2).
func ParseRange(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Prefix{}, errors.New("empty address range")
	}
	if !strings.Contains(s, "/") {
		addr, err := netip.ParseAddr(s)
		if err != nil || addr.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("invalid address %q", s)
		}
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Zone() == "" {
		return p.Masked(), nil
	}
	base, bitsStr, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(base, ":") {
		return netip.Prefix{}, fmt.Errorf("invalid CIDR %q", s)
	}
	bits, err := strconv.Atoi(bitsStr)
	if err != nil || bits < 0 || bits > 32 {
		return netip.Prefix{}, fmt.Errorf("invalid prefix length in %q", s)
	}
	parts := strings.Split(base, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return netip.Prefix{}, fmt.Errorf("invalid short CIDR %q", s)
	}
	var octets [4]byte
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 || strconv.Itoa(n) != part {
			return netip.Prefix{}, fmt.Errorf("invalid octet in %q", s)
		}
		octets[i] = byte(n)
	}
	return netip.PrefixFrom(netip.AddrFrom4(octets), bits).Masked(), nil
}
