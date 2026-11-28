package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

type ruleKind int

const (
	ruleExactHost ruleKind = iota
	ruleWildcardHost
	rulePrefix
)

type rule struct {
	raw        string
	kind       ruleKind
	host       string       // normalized lowercase host or suffix (without "*.")
	prefix     netip.Prefix // set when kind == rulePrefix
	port       int          // 0 means any port
	explicitIP bool         // true when target named an IP or CIDR explicitly
}

func (r rule) matchesHost(host string) bool {
	if r.kind == rulePrefix {
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return false
		}
		return r.prefix.Contains(addr.WithZone("").Unmap())
	}
	if r.kind == ruleExactHost {
		return host == r.host
	}
	// Wildcard "*.example.com" matches subdomains such as "a.example.com" and
	// "a.b.example.com", never the apex "example.com".
	return len(host) > len(r.host)+1 &&
		strings.HasSuffix(host, r.host) &&
		host[len(host)-len(r.host)-1] == '.'
}

func (r rule) matches(host string, port int) bool {
	if r.port != 0 && r.port != port {
		return false
	}
	return r.matchesHost(host)
}

func matchRule(rules []rule, host string, port int) (rule, bool) {
	for _, r := range rules {
		if r.matches(host, port) {
			return r, true
		}
	}
	return rule{}, false
}

func matchHostOnlyDeny(rules []rule, host string) (rule, bool) {
	for _, r := range rules {
		if r.port == 0 && r.matchesHost(host) {
			return r, true
		}
	}
	return rule{}, false
}

func matchAllowedHost(s *policySnapshot, list []rule, host string) (rule, bool) {
	for _, r := range list {
		if !r.matchesHost(host) {
			continue
		}
		port := r.port
		if port == 0 {
			port = 443
		}
		if _, denied := matchRule(s.locks, host, port); denied {
			continue
		}
		if _, denied := matchRule(s.denies, host, port); denied {
			continue
		}
		return r, true
	}
	return rule{}, false
}

func matchesAnyLock(locks []rule, candidate rule) (bool, string) {
	for _, l := range locks {
		if l.port != 0 && candidate.port != 0 && l.port != candidate.port {
			continue
		}
		switch {
		case candidate.kind == ruleExactHost && l.matchesHost(candidate.host):
			return true, l.raw
		case candidate.kind == ruleWildcardHost && l.kind == ruleWildcardHost &&
			(candidate.host == l.host || strings.HasSuffix(candidate.host, "."+l.host)):
			return true, l.raw
		case candidate.kind == rulePrefix && l.kind == rulePrefix && l.prefix.Overlaps(candidate.prefix):
			return true, l.raw
		}
	}
	return false, ""
}

func parseRules(raw []string, isDeny bool) ([]rule, error) {
	out := make([]rule, 0, len(raw))
	for _, item := range raw {
		r, err := parseRule(item, isDeny)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func parseRule(raw string, isDeny bool) (rule, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return rule{}, errors.New("empty network rule. Try: pass a host such as api.example.com")
	}
	hostPart, port, err := splitTargetHostPort(s, isDeny)
	if err != nil {
		return rule{}, err
	}
	if pfx, ok, err := parseIPOrCIDR(hostPart); err != nil {
		return rule{}, err
	} else if ok {
		return rule{raw: s, kind: rulePrefix, prefix: pfx, port: port, explicitIP: true}, nil
	}
	if strings.HasPrefix(hostPart, "*.") {
		suffix, err := normalizeHost(hostPart[2:])
		if err != nil {
			return rule{}, fmt.Errorf("invalid wildcard host %q: %w", raw, err)
		}
		if _, ipErr := netip.ParseAddr(suffix); ipErr == nil {
			return rule{}, fmt.Errorf("wildcard %q cannot match an IP address. Try: use a CIDR prefix instead", raw)
		}
		if !isDeny && isPublicSuffix(suffix) {
			return rule{}, fmt.Errorf("wildcard %q is too broad (public or shared suffix). Try: name a more specific domain", raw)
		}
		return rule{raw: s, kind: ruleWildcardHost, host: suffix, port: port}, nil
	}
	host, err := normalizeHost(hostPart)
	if err != nil {
		return rule{}, fmt.Errorf("invalid host %q: %w", raw, err)
	}
	return rule{raw: s, kind: ruleExactHost, host: host, port: port}, nil
}

func splitTargetHostPort(s string, isDeny bool) (string, int, error) {
	defaultPort := 443
	if isDeny {
		defaultPort = 0
	}
	// Bracketed IPv6 literal with optional :port.
	if strings.HasPrefix(s, "[") {
		if h, p, err := net.SplitHostPort(s); err == nil {
			port, err := parsePort(p)
			return h, port, err
		}
		if strings.HasSuffix(s, "]") {
			return s[1 : len(s)-1], defaultPort, nil
		}
		return "", 0, fmt.Errorf("invalid IPv6 target %q. Try: wrap IPv6 addresses in brackets, such as [2001:db8::1]:443", s)
	}
	// Bare IPv6 or IPv6 CIDR (contains more than one colon).
	if strings.Count(s, ":") > 1 {
		return s, defaultPort, nil
	}
	// Single colon separates host/CIDR and port.
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		port, err := parsePort(s[i+1:])
		if err != nil {
			return "", 0, fmt.Errorf("invalid port in %q: %w", s, err)
		}
		return s[:i], port, nil
	}
	return s, defaultPort, nil
}

func parsePort(s string) (int, error) {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("port %q must be between 1 and 65535. Try: pass a valid TCP port", s)
	}
	return p, nil
}

func parseIPOrCIDR(s string) (netip.Prefix, bool, error) {
	if strings.ContainsRune(s, '/') {
		pfx, err := parseCIDR(s)
		if err != nil {
			return netip.Prefix{}, false, err
		}
		return pfx, true, nil
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		clean := addr.WithZone("").Unmap()
		return netip.PrefixFrom(clean, clean.BitLen()), true, nil
	}
	return netip.Prefix{}, false, nil
}

func isPublicSuffix(domain string) bool {
	suffix, _ := publicsuffix.PublicSuffix(domain)
	return suffix == domain
}

func normalizeHost(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.WithZone("").Unmap().String(), nil
	}
	s = strings.TrimSuffix(strings.ToLower(s), ".")
	if s == "" || len(s) > 253 {
		return "", fmt.Errorf("invalid host name %q. Try: use a valid DNS host name", raw)
	}
	for label := range strings.SplitSeq(s, ".") {
		if !validLabel(label) {
			return "", fmt.Errorf("invalid host name %q. Try: use a valid DNS host name", raw)
		}
	}
	return s, nil
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := range len(label) {
		c := label[i]
		ok := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}
