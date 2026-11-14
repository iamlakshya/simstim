package egress

import (
	"net/netip"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
)

// A finite method witness set shares the canonical evaluator with DNS and
// CONNECT eligibility. The extra token witnesses the complement of all
// configured exact method sets. No precheck reserves a one-use grant.
func dnsCandidateMethods(rules []compiledRule) []string {
	methods := []string{"GET", "HEAD"}
	seen := map[string]bool{"GET": true, "HEAD": true}
	for _, rule := range rules {
		if rule.methods == nil {
			continue
		}
		for _, method := range *rule.methods {
			if !seen[method] {
				seen[method] = true
				methods = append(methods, method)
			}
		}
	}
	outside := "Policy-Probe"
	for seen[outside] {
		outside += "-X"
	}
	return append(methods, outside)
}

func (e *Engine) httpHostRules(host string, port int) []compiledRule {
	addr, err := netip.ParseAddr(host)
	at := e.now()
	var out []compiledRule
	collect := func(rules []compiledRule) {
		for _, rule := range rules {
			if e.matchesLocked(rule, host, addr, err == nil, port, rule.path, at) {
				out = append(out, rule)
			}
		}
	}
	collect(e.locks)
	for _, layer := range e.layers {
		collect(layer.rules)
	}
	collect(e.agentRules)
	collect(e.balanced)
	return out
}

func (e *Engine) permitsIntercept(host string, port int) bool {
	rules := e.httpHostRules(host, port)
	methods := dnsCandidateMethods(rules)
	for _, path := range dnsCandidatePaths(rules) {
		if path == "" {
			continue
		}
		for _, method := range methods {
			if e.evaluateHTTP(Request{Host: host, Port: port, Path: path, Method: method}, nil, evalPrecheck).Allow {
				return true
			}
		}
	}
	return false
}

func (e *Engine) hasInspectionRule(host string, port int) bool {
	for _, rule := range e.httpHostRules(host, port) {
		if rule.methods != nil && len(*rule.methods) == 0 {
			continue
		}
		if rule.path != "" || rule.methods != nil {
			return true
		}
	}
	// A method/path-scoped address exception can be needed by a DNS name.
	// Until the checked dial resolves that name, opaque splicing cannot
	// establish which scope would constrain the backing address.
	needsNumericInspection := func(rules []compiledRule) bool {
		for _, rule := range rules {
			if rule.methods != nil && len(*rule.methods) == 0 {
				continue
			}
			if rule.isNumeric && (rule.path != "" || rule.methods != nil) {
				return true
			}
		}
		return false
	}
	if needsNumericInspection(e.locks) {
		return true
	}
	for _, layer := range e.layers {
		if needsNumericInspection(layer.rules) {
			return true
		}
	}
	return false
}

func explicitPathAllowance(decision Decision) bool {
	if !decision.Allow || decision.Match == nil || decision.Match.Kind == api.LayerBuiltin || decision.Target == "" {
		return false
	}
	_, path, err := splitTargetPath(decision.Target)
	return err == nil && strings.HasPrefix(path, "/")
}

// DNS needs a read-only witness for method/path-scoped numeric exceptions too.
// A name allowance and an address allowance must agree on one actual request,
// rather than separately finding unrelated methods or paths which cannot run.
func (e *Engine) precheckResolvedAddr(host string, addr netip.Addr, port int) bool {
	rules := e.httpHostRules(host, port)
	at := e.now()
	collect := func(candidates []compiledRule) {
		for _, rule := range candidates {
			if rule.isNumeric && e.matchesLocked(rule, addr.Unmap().String(), addr, true, port, rule.path, at) {
				rules = append(rules, rule)
			}
		}
	}
	collect(e.locks)
	for _, layer := range e.layers {
		collect(layer.rules)
	}
	methods := dnsCandidateMethods(rules)
	requiresHTTP := e.requiresHTTPWitness(host, port)
	for _, path := range dnsCandidatePaths(rules) {
		if path == "" && requiresHTTP {
			continue
		}
		for _, method := range methods {
			request := Request{Host: host, Port: port, Path: path, Method: method, Now: at}
			var decision Decision
			if path == "" {
				decision = e.evaluate(request, nil, evalPrecheck)
			} else {
				decision = e.evaluateHTTP(request, nil, evalPrecheck)
			}
			if decision.Allow && e.evaluateAddrRequest(addr, port, nil, evalPrecheck, &request) {
				return true
			}
		}
	}
	return false
}
