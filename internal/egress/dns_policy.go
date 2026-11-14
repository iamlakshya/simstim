package egress

import (
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// dnsHostVerdict summarises the port and path projection of host policy for a
// single DNS query without mutating once-grant state.
type dnsHostVerdict struct {
	allowedPorts []int
	hasUnmatched bool
	unknown      api.Unknown
}

func (v dnsHostVerdict) permitsReal() bool {
	return len(v.allowedPorts) > 0
}

func (v dnsHostVerdict) permitsSynthetic() bool {
	if len(v.allowedPorts) > 0 {
		return true
	}
	if !v.hasUnmatched {
		return false
	}
	switch v.unknown {
	case api.UnknownAsk, api.UnknownAllow, api.UnknownClassify:
		return true
	default:
		return false
	}
}

// evaluateDNS projects host policy across candidate port and path witnesses at
// time at without consuming once rules or emitting flow state.
func (e *Engine) evaluateDNS(host string, at time.Time) dnsHostVerdict {
	if at.IsZero() {
		at = e.now()
	}
	hostRules := e.dnsHostRules(host, at)
	ports := e.dnsCandidatePorts(hostRules, at)
	paths := dnsCandidatePaths(hostRules)
	methods := dnsCandidateMethods(hostRules)

	out := dnsHostVerdict{unknown: e.unknown}
	for _, port := range ports {
		portAllowed := false
		requiresHTTP := e.requiresHTTPWitness(host, port)
		for _, path := range paths {
			if path == "" && requiresHTTP {
				continue
			}
			// Use the flow engine's read-only mode so DNS shares its locks,
			// precedence, expiry and once-grant availability. This call takes
			// the ledger lock itself and never claims a grant.
			for _, method := range methods {
				request := Request{Host: host, Port: port, Method: method, Path: path, Now: at}
				var decision Decision
				if path == "" {
					decision = e.evaluate(request, nil, evalPrecheck)
				} else {
					decision = e.evaluateHTTP(request, nil, evalPrecheck)
				}
				switch {
				case decision.Allow:
					portAllowed = true
				case decision.unmatched:
					out.hasUnmatched = true
				}
			}
		}
		if portAllowed {
			out.allowedPorts = append(out.allowedPorts, port)
		}
	}
	return out
}

// Engine rules are immutable after publication. Witness collection includes
// unavailable once rules. Only the shared evaluator decides availability.
func (e *Engine) dnsHostRules(host string, at time.Time) []compiledRule {
	var out []compiledRule
	collect := func(rs []compiledRule) {
		for _, r := range rs {
			if dnsRuleMatchesHost(r, host, at) {
				out = append(out, r)
			}
		}
	}
	collect(e.locks)
	for _, layer := range e.layers {
		collect(layer.rules)
	}
	collect(e.agentRules)
	if e.preset == api.PresetBalanced {
		collect(e.balanced)
	}
	return out
}

func dnsRuleMatchesHost(r compiledRule, host string, at time.Time) bool {
	if r.isNumeric {
		return false
	}
	if !r.until.IsZero() && !at.Before(r.until) {
		return false
	}
	if r.wildcard {
		return len(host) > len(r.host)+1 && strings.HasSuffix(host, "."+r.host)
	}
	return host == r.host
}

func (e *Engine) dnsCandidatePorts(hostRules []compiledRule, at time.Time) []int {
	portSet := map[int]bool{443: true}
	ports := []int{443}
	addPort := func(p int) {
		if p >= 1 && p <= 65535 && !portSet[p] {
			portSet[p] = true
			ports = append(ports, p)
		}
	}
	for _, r := range hostRules {
		want := r.port
		if want == 0 && r.allow {
			want = 443
		}
		addPort(want)
	}
	// Include ports named on live numeric allow rules so CheckAddr sees any
	// custom port opened under open or multi-port host grants.
	for _, layer := range e.layers {
		for _, r := range layer.rules {
			if !r.isNumeric || !r.allow {
				continue
			}
			if !r.until.IsZero() && !at.Before(r.until) {
				continue
			}
			want := r.port
			if want == 0 {
				want = 443
			}
			addPort(want)
		}
	}
	if !portSet[80] {
		addPort(80)
		return ports
	}
	for p := 1; p <= 65535; p++ {
		if !portSet[p] {
			addPort(p)
			break
		}
	}
	return ports
}

func dnsCandidatePaths(hostRules []compiledRule) []string {
	pathSet := map[string]bool{"": true, "/": true}
	paths := []string{"", "/"}
	addPath := func(p string) {
		if !pathSet[p] {
			pathSet[p] = true
			paths = append(paths, p)
		}
	}

	outside := "/dns-probe"
	for collidesWithRulePath(hostRules, outside, "/") {
		outside += "-x"
	}
	addPath(outside)

	for _, r := range hostRules {
		if !r.allow || r.path == "" {
			continue
		}
		addPath(r.path)
		if r.pathPrefix {
			child := r.path + "dns-probe"
			for collidesWithRulePath(hostRules, child, r.path) {
				child += "-x"
			}
			addPath(child)
		}
	}
	return paths
}

func collidesWithRulePath(rules []compiledRule, candidate, basePrefix string) bool {
	for _, r := range rules {
		if r.path != "" && len(r.path) > len(basePrefix) && matchesRulePath(r, candidate) {
			return true
		}
	}
	return false
}
