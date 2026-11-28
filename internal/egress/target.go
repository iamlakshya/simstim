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
	method     string       // empty means any HTTP method; uppercase when set
	path       string       // empty means any path; starts with "/" when set
	explicitIP bool         // true when target named an IP or CIDR explicitly
}

func (r rule) isHTTPScoped() bool {
	return r.method != "" || r.path != ""
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

func (r rule) matchesHTTP(host string, port int, method, canonPath string) bool {
	if !r.matches(host, port) {
		return false
	}
	if r.method != "" && !strings.EqualFold(r.method, method) {
		return false
	}
	if r.path != "" && !matchPathPattern(r.path, canonPath) {
		return false
	}
	return true
}

func matchRule(rules []rule, host string, port int) (rule, bool) {
	for _, r := range rules {
		if r.matches(host, port) {
			return r, true
		}
	}
	return rule{}, false
}

func matchWholeHostRule(rules []rule, host string, port int) (rule, bool) {
	for _, r := range rules {
		if !r.isHTTPScoped() && r.matches(host, port) {
			return r, true
		}
	}
	return rule{}, false
}

func matchHTTPRule(rules []rule, host string, port int, method, canonPath string) (rule, bool) {
	for _, r := range rules {
		if r.matchesHTTP(host, port, method, canonPath) {
			return r, true
		}
	}
	return rule{}, false
}

func matchScopedHTTPRule(rules []rule, host string, port int, method, canonPath string) (rule, bool) {
	for _, r := range rules {
		if r.isHTTPScoped() && r.matchesHTTP(host, port, method, canonPath) {
			return r, true
		}
	}
	return rule{}, false
}

func matchHostAny(rules []rule, host string) bool {
	for _, r := range rules {
		if r.matchesHost(host) {
			return true
		}
	}
	return false
}

func matchHostOnlyDeny(rules []rule, host string) (rule, bool) {
	for _, r := range rules {
		if r.port == 0 && !r.isHTTPScoped() && r.matchesHost(host) {
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
		if _, denied := matchWholeHostRule(s.locks, host, port); denied {
			continue
		}
		if _, denied := matchWholeHostRule(s.denies, host, port); denied {
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
		hostOverlap := false
		switch {
		case candidate.kind == ruleExactHost && l.matchesHost(candidate.host):
			hostOverlap = true
		case candidate.kind == ruleWildcardHost && l.kind == ruleWildcardHost &&
			(candidate.host == l.host || strings.HasSuffix(candidate.host, "."+l.host)):
			hostOverlap = true
		case candidate.kind == rulePrefix && l.kind == rulePrefix && l.prefix.Overlaps(candidate.prefix):
			hostOverlap = true
		}
		if !hostOverlap {
			continue
		}
		if !l.isHTTPScoped() {
			return true, l.raw
		}
		// A scoped lock blocks an allow targeting the same method/path; an
		// unscoped allow can open other methods while the lock still wins on
		// DecideHTTP for the locked method/path.
		if candidate.isHTTPScoped() &&
			(l.method == "" || strings.EqualFold(l.method, candidate.method)) &&
			(l.path == "" || l.path == candidate.path || matchPathPattern(l.path, candidate.path)) {
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
	method, rest, err := splitRuleMethod(s)
	if err != nil {
		return rule{}, err
	}
	targetPart, pathPart, err := splitRulePath(rest)
	if err != nil {
		return rule{}, err
	}
	hostPart, port, err := splitTargetHostPort(targetPart, isDeny)
	if err != nil {
		return rule{}, err
	}
	if pfx, ok, err := parseIPOrCIDR(hostPart); err != nil {
		return rule{}, err
	} else if ok {
		return rule{raw: s, kind: rulePrefix, prefix: pfx, port: port, method: method, path: pathPart, explicitIP: true}, nil
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
		return rule{raw: s, kind: ruleWildcardHost, host: suffix, port: port, method: method, path: pathPart}, nil
	}
	host, err := normalizeHost(hostPart)
	if err != nil {
		return rule{}, fmt.Errorf("invalid host %q: %w", raw, err)
	}
	return rule{raw: s, kind: ruleExactHost, host: host, port: port, method: method, path: pathPart}, nil
}

func splitRuleMethod(s string) (method, rest string, err error) {
	first, after, hasSpace := strings.Cut(s, " ")
	if !hasSpace {
		return "", s, nil
	}
	after = strings.TrimSpace(after)
	if after == "" || strings.ContainsAny(after, " \t") {
		return "", "", fmt.Errorf("invalid network rule %q. Try: use \"METHOD host/path\" such as \"GET api.example.com/v1/*\"", s)
	}
	m := strings.ToUpper(strings.TrimSpace(first))
	switch m {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return m, after, nil
	default:
		return "", "", fmt.Errorf("unsupported HTTP method %q in rule %q. Try: use GET, HEAD, POST, PUT, PATCH, DELETE or OPTIONS", first, s)
	}
}

func splitRulePath(s string) (target, pathPat string, err error) {
	slash := strings.IndexByte(s, '/')
	if slash < 0 {
		return s, "", nil
	}
	// Preserve full and short CIDR notation with optional :port (such as
	// 10/8, 10/8:5432, 198.51.100.0/24:443 or 2001:db8::/32).
	if isCIDRTarget(s) {
		return s, "", nil
	}
	left, right := s[:slash], s[slash:]
	if left == "" {
		return "", "", fmt.Errorf("missing host before path in rule %q. Try: name a host before the path, such as api.example.com%s", s, right)
	}
	if err := validatePathPattern(right); err != nil {
		return "", "", fmt.Errorf("invalid path pattern %q in rule %q: %w", right, s, err)
	}
	return left, right, nil
}

func isCIDRTarget(s string) bool {
	if strings.Count(s, "/") != 1 {
		return false
	}
	hostPart, _, err := splitTargetHostPort(s, true)
	if err != nil {
		return false
	}
	_, err = parseCIDR(hostPart)
	return err == nil
}

func validatePathPattern(pat string) error {
	if !strings.HasPrefix(pat, "/") {
		return errors.New("path pattern must start with '/'. Try: prefix the path with '/'")
	}
	if strings.ContainsAny(pat, "?#\\\r\n\x00") || hasCTL(pat) {
		return errors.New("path pattern must not contain query strings, fragments, backslashes or control characters. Try: match the canonical path only")
	}
	for seg := range strings.SplitSeq(pat, "/") {
		if seg == "." || seg == ".." {
			return errors.New("path pattern must not contain '.' or '..' segments. Try: write a canonical path pattern")
		}
	}
	return nil
}

// matchPathPattern matches canonPath against pattern:
//   - exact path ("/v1/items")
//   - trailing slash prefix ("/v1/" matches "/v1/" and "/v1/a/b")
//   - single-segment glob "*" ("/repos/*/issues")
//   - multi-segment glob "**" ("/v1/**" matches "/v1", "/v1/" and "/v1/a/b")
func matchPathPattern(pattern, canonPath string) bool {
	if pattern == "" || pattern == "/**" {
		return true
	}
	if strings.HasSuffix(pattern, "/") && !strings.Contains(pattern, "*") {
		return strings.HasPrefix(canonPath, pattern)
	}
	pSegs := splitPathSegments(pattern)
	cSegs := splitPathSegments(canonPath)
	return matchSegments(pSegs, cSegs)
}

func splitPathSegments(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func matchSegments(pat, pathSegs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// Collapse consecutive "**" to keep matching bounded.
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(pathSegs); i++ {
				if matchSegments(pat[1:], pathSegs[i:]) {
					return true
				}
			}
			return false
		}
		if len(pathSegs) == 0 {
			return false
		}
		if !matchSegmentWildcard(pat[0], pathSegs[0]) {
			return false
		}
		pat = pat[1:]
		pathSegs = pathSegs[1:]
	}
	return len(pathSegs) == 0
}

func matchSegmentWildcard(pat, seg string) bool {
	if pat == "*" {
		return true
	}
	if !strings.Contains(pat, "*") {
		return pat == seg
	}
	// Simple '*' wildcard inside a single segment (e.g. "pkg-*.tgz").
	parts := strings.Split(pat, "*")
	if !strings.HasPrefix(seg, parts[0]) {
		return false
	}
	rem := seg[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		idx := strings.Index(rem, mid)
		if idx < 0 {
			return false
		}
		rem = rem[idx+len(mid):]
	}
	return strings.HasSuffix(rem, parts[len(parts)-1])
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
