package egress

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"

	"github.com/iamlakshya/simstim/internal/brand"
)

type spliceFile struct {
	Entries []spliceEntry `toml:"entries"`
}

type spliceEntry struct {
	Host   string `toml:"host"`
	Reason string `toml:"reason"`
	Source string `toml:"source"`
}

type classFile struct {
	Name    string       `toml:"name"`
	Methods []string     `toml:"methods"`
	Entries []classEntry `toml:"entries"`
}

type classEntry struct {
	Host   string `toml:"host"`
	Source string `toml:"source"`
}

type hostClass struct {
	name    string
	methods map[string]bool
	hosts   []rule
}

type builtinRuleSets struct {
	spliceOnly []rule
	classes    []hostClass
}

var (
	loadRuleSetsOnce sync.Once
	loadedRuleSets   builtinRuleSets
	errLoadRuleSets  error
)

func builtinRules() (builtinRuleSets, error) {
	loadRuleSetsOnce.Do(func() {
		var out builtinRuleSets
		spliceRules, err := loadSpliceFile("rules/splice.toml")
		if err != nil {
			errLoadRuleSets = err
			return
		}
		out.spliceOnly = spliceRules

		for _, file := range []string{"rules/registries.toml", "rules/object_stores.toml"} {
			cls, err := loadClassFile(file)
			if err != nil {
				errLoadRuleSets = err
				return
			}
			out.classes = append(out.classes, cls)
		}
		loadedRuleSets = out
	})
	return loadedRuleSets, errLoadRuleSets
}

func loadSpliceFile(name string) ([]rule, error) {
	raw, err := rulesFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("egress: read embedded %s: %w", name, err)
	}
	var sf spliceFile
	if err := toml.Unmarshal(raw, &sf); err != nil {
		return nil, fmt.Errorf("egress: decode embedded %s: %w", name, err)
	}
	out := make([]rule, 0, len(sf.Entries))
	for _, e := range sf.Entries {
		// Splice-only entries may name shared suffixes (such as *.mtls.googleapis.com)
		// and match any port.
		r, err := parseRule(e.Host, true)
		if err != nil {
			return nil, fmt.Errorf("egress: %s entry %q: %w", name, e.Host, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func loadClassFile(name string) (hostClass, error) {
	raw, err := rulesFS.ReadFile(name)
	if err != nil {
		return hostClass{}, fmt.Errorf("egress: read embedded %s: %w", name, err)
	}
	var cf classFile
	if err := toml.Unmarshal(raw, &cf); err != nil {
		return hostClass{}, fmt.Errorf("egress: decode embedded %s: %w", name, err)
	}
	methods := make(map[string]bool, len(cf.Methods))
	for _, m := range cf.Methods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if m != "" {
			methods[m] = true
		}
	}
	hosts := make([]rule, 0, len(cf.Entries))
	for _, e := range cf.Entries {
		// Class entries may name shared suffixes such as *.s3.amazonaws.com and
		// apply regardless of port so a non-443 port cannot bypass read-only rules.
		r, err := parseRule(e.Host, true)
		if err != nil {
			return hostClass{}, fmt.Errorf("egress: %s entry %q: %w", name, e.Host, err)
		}
		hosts = append(hosts, r)
	}
	return hostClass{name: cf.Name, methods: methods, hosts: hosts}, nil
}

// IsSpliceOnly reports whether rawHost is on the embedded splice-only list
// (rules/splice.toml). Splice-only hosts are never intercepted and never
// receive a bound secret (spec 7.2).
func (e *Engine) IsSpliceOnly(rawHost string) bool {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return false
	}
	s := e.snap.Load()
	return matchHostAny(s.spliceOnly, host)
}

func (s *policySnapshot) matchClass(host string) (hostClass, bool) {
	for _, cls := range s.classes {
		if matchHostAny(cls.hosts, host) {
			return cls, true
		}
	}
	return hostClass{}, false
}

// ShouldIntercept reports whether an allowed HTTPS connection to (rawHost, port)
// must be intercepted with the session CA rather than spliced (spec 7.2).
// Splice-only hosts and gateway-only model hosts are never intercepted.
func (e *Engine) ShouldIntercept(rawHost string, port int) (bool, string) {
	host, err := normalizeHost(rawHost)
	if err != nil || port < 1 || port > 65535 {
		return false, ""
	}
	s := e.snap.Load()
	if matchHostAny(s.spliceOnly, host) {
		return false, "splice_only"
	}
	if s.isGatewayHost(host) {
		return false, "gateway"
	}
	if hasScopedRuleForHost(s, host, port) {
		return true, "rule"
	}
	if cls, ok := s.matchClass(host); ok {
		return true, "class:" + cls.name
	}
	return false, ""
}

// HasPathRule reports whether any lock, deny, allow or live grant for
// (rawHost, port) constrains the request path. When true, encoded slashes
// (%2f) are refused in canonical path checks so path rules cannot be bypassed.
func (e *Engine) HasPathRule(rawHost string, port int) bool {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return false
	}
	s := e.snap.Load()
	for _, list := range [][]rule{s.locks, s.denies, s.allows, s.grants} {
		for _, r := range list {
			if r.path != "" && r.matches(host, port) {
				return true
			}
		}
	}
	return false
}

func hasScopedRuleForHost(s *policySnapshot, host string, port int) bool {
	for _, list := range [][]rule{s.locks, s.denies, s.allows, s.grants} {
		for _, r := range list {
			if r.isHTTPScoped() && r.matches(host, port) {
				return true
			}
		}
	}
	return false
}

// DecideHTTP evaluates one canonicalized HTTP request (either inside an
// intercepted TLS tunnel or over the plain HTTP proxy) under deny-wins policy,
// explicit method/path rules and built-in class rules (registries and object
// stores limited to GET and HEAD by default).
func (e *Engine) DecideHTTP(req Request) Decision {
	host, err := normalizeHost(req.Host)
	if err != nil {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: err.Error()}
	}
	if req.Port < 1 || req.Port > 65535 {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: fmt.Sprintf("invalid port %d", req.Port)}
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: "missing HTTP method"}
	}
	canonPath := req.Path
	if canonPath == "" {
		canonPath = "/"
	}

	s := e.snap.Load()
	if r, ok := matchHTTPRule(s.locks, host, req.Port, method, canonPath); ok {
		return Decision{
			Action: ActionDeny,
			Rule:   "lock:" + r.raw,
			Why:    e.denyMethodHint(host, req.Port, method, canonPath, "denied by lock"),
		}
	}
	if r, ok := matchHTTPRule(s.denies, host, req.Port, method, canonPath); ok {
		return Decision{
			Action: ActionDeny,
			Rule:   "deny:" + r.raw,
			Why:    e.denyMethodHint(host, req.Port, method, canonPath, "denied by policy"),
		}
	}
	if s.isGatewayHost(host) {
		return Decision{Action: ActionDeny, Rule: "gateway:" + host, Why: gatewayDirectDenyHint(host)}
	}

	// Explicit scoped allow or grant (naming a method or path) matches directly
	// and can deliberately grant writes to a class-restricted host.
	if r, ok := matchScopedHTTPRule(s.allows, host, req.Port, method, canonPath); ok {
		return Decision{Action: ActionAllow, Mode: ModeIntercept, Rule: "allow:" + r.raw}
	}
	if r, ok := matchScopedHTTPRule(s.grants, host, req.Port, method, canonPath); ok {
		return Decision{Action: ActionAllow, Mode: ModeIntercept, Rule: "grant:" + r.raw}
	}

	// Otherwise check whether the host itself is permitted by an unscoped rule,
	// agent host, preset or unknown=allow.
	hostRule, hostAllowed := matchUnscopedHostAllow(s, host, req.Port)
	if !hostAllowed {
		if hasScopedAllowForHost(s, host, req.Port) {
			return Decision{
				Action: ActionDeny,
				Rule:   "method_path",
				Why:    e.denyMethodHint(host, req.Port, method, canonPath, "method or path not allowed"),
			}
		}
		if s.unknown == UnknownAsk {
			return Decision{Action: ActionDeny, Rule: "unknown:ask", Why: e.DenyHint(host, req.Port)}
		}
		return Decision{Action: ActionDeny, Rule: "unknown:deny", Why: e.DenyHint(host, req.Port)}
	}

	// Built-in class rules restrict package registries and object stores to GET
	// and HEAD unless an explicit scoped allow rule above granted the method.
	if cls, ok := s.matchClass(host); ok && !cls.methods[method] {
		return Decision{
			Action: ActionDeny,
			Rule:   "class:" + cls.name,
			Why:    e.denyClassHint(host, req.Port, method, canonPath, cls.name),
		}
	}

	dec := Decision{Action: ActionAllow, Mode: ModeIntercept, Rule: hostRule}
	if hostRule == "unknown:allow" {
		dec.Why = "allowed by unknown=allow"
	}
	return dec
}

func matchUnscopedHostAllow(s *policySnapshot, host string, port int) (string, bool) {
	if r, ok := matchWholeHostRule(s.allows, host, port); ok {
		return "allow:" + r.raw, true
	}
	if r, ok := matchWholeHostRule(s.grants, host, port); ok {
		return "grant:" + r.raw, true
	}
	if _, ok := matchWholeHostRule(s.agentRules, host, port); ok {
		return "agent:" + s.agent, true
	}
	if s.preset == PresetOpen {
		return "preset:open", true
	}
	if _, ok := matchWholeHostRule(s.presetList, host, port); ok {
		return "preset:" + string(s.preset), true
	}
	if s.unknown == UnknownAllow {
		return "unknown:allow", true
	}
	return "", false
}

func hasScopedAllowForHost(s *policySnapshot, host string, port int) bool {
	for _, list := range [][]rule{s.allows, s.grants} {
		for _, r := range list {
			if r.isHTTPScoped() && r.matches(host, port) {
				return true
			}
		}
	}
	return false
}

func (e *Engine) denyClassHint(host string, port int, method, canonPath, className string) string {
	target := formatHostPort(host, port)
	label := "read-only host"
	switch className {
	case "registry":
		label = "package registry is read-only (GET and HEAD only)"
	case "object_store":
		label = "object store is read-only (GET and HEAD only)"
	}
	sess := e.Session()
	grant := fmt.Sprintf("%q", method+" "+target+"/*")
	if sess == "" {
		return fmt.Sprintf("%s: %s %s at %s is not allowed (%s). Try: %s policy allow %s", brand.Name, method, canonPath, target, label, brand.Name, grant)
	}
	return fmt.Sprintf("%s: %s %s at %s is not allowed in %s (%s). Try: %s policy allow %s --in %s", brand.Name, method, canonPath, target, sess, label, brand.Name, grant, sess)
}

func (e *Engine) denyMethodHint(host string, port int, method, canonPath, reason string) string {
	target := formatHostPort(host, port)
	sess := e.Session()
	grant := fmt.Sprintf("%q", method+" "+target+canonPath)
	if sess == "" {
		return fmt.Sprintf("%s: %s %s at %s is not allowed (%s). Try: %s policy allow %s", brand.Name, method, canonPath, target, reason, brand.Name, grant)
	}
	return fmt.Sprintf("%s: %s %s at %s is not allowed in %s (%s). Try: %s policy allow %s --in %s", brand.Name, method, canonPath, target, sess, reason, brand.Name, grant, sess)
}

func formatHostPort(host string, port int) string {
	if port != 0 && port != 443 {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	return host
}
