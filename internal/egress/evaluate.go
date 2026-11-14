package egress

import (
	"net/netip"
	"time"
)

// DecideWithPermit reserves the grant chosen for one flow.
func (e *Engine) DecideWithPermit(req Request, p *Permit) Decision {
	return e.evaluate(req, p, evalReserve)
}

// Revalidate evaluates current policy without consuming another grant.
func (e *Engine) Revalidate(req Request, p *Permit) Decision {
	return e.evaluate(req, p, evalRevalidate)
}

// PrecheckAddr checks candidate eligibility without claiming a grant. A nil
// permit gives read-only availability for DNS projection and similar checks.
func (e *Engine) PrecheckAddr(addr netip.Addr, port int, p *Permit) bool {
	return e.evaluateAddr(addr, port, p, evalPrecheck)
}

// ReserveAddr claims any numeric exception needed by a literal dial.
func (e *Engine) ReserveAddr(addr netip.Addr, port int, p *Permit) bool {
	return e.evaluateAddr(addr, port, p, evalReserve)
}

// RevalidateAddr checks the receiver's current backing-address permission.
func (e *Engine) RevalidateAddr(addr netip.Addr, port int, p *Permit) bool {
	return e.evaluateAddr(addr, port, p, evalRevalidate)
}

func (e *Engine) evaluate(req Request, p *Permit, mode evalMode) Decision {
	host, err := NormaliseHost(req.Host)
	if err != nil || req.Port < 1 || req.Port > 65535 || e.ledger == nil {
		return Decision{Rule: "invalid"}
	}
	path, err := canonicalPath(req.Path)
	if err != nil {
		return Decision{Rule: "invalid"}
	}
	method := req.Method
	if path == "" {
		method = ""
	}
	at := req.Now
	if at.IsZero() {
		at = e.now()
	}
	addr, addrErr := netip.ParseAddr(host)
	isIP := addrErr == nil
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	if !e.validPermit(p, mode) {
		return Decision{Rule: "invalid"}
	}
	for _, lk := range e.locks {
		if matchesRuleMethod(lk, method) && e.matchesLocked(lk, host, addr, isIP, req.Port, path, at) {
			return Decision{Rule: lk.id, RuleID: lk.id, Target: formatCompiledTarget(lk), Match: &lk.ref}
		}
	}
	for _, layer := range e.layers {
		allow, denied := e.choose(layer.rules, p, mode, func(r compiledRule) bool {
			return matchesRuleMethod(r, method) && e.matchesLocked(r, host, addr, isIP, req.Port, path, at)
		})
		if denied != nil {
			match := denied.ref
			return Decision{Rule: string(layer.kind) + ":" + denied.id, RuleID: denied.id, Target: formatCompiledTarget(*denied), Match: &match}
		}
		if allow != nil {
			e.useOnce(allow, p, mode)
			match := allow.ref
			return Decision{Allow: true, Rule: string(layer.kind) + ":" + allow.id, RuleID: allow.id, Target: formatCompiledTarget(*allow), Match: &match}
		}
	}
	for _, r := range e.agentRules {
		if matchesRuleMethod(r, method) && e.matchesLocked(r, host, addr, isIP, req.Port, path, at) {
			return Decision{Allow: true, Rule: r.id, RuleID: r.id, Target: formatCompiledTarget(r), Match: &r.ref}
		}
	}
	return e.decidePreset(host, addr, isIP, req.Port, path, at)
}

func (e *Engine) validPermit(p *Permit, mode evalMode) bool {
	if p == nil {
		return mode == evalStandalone || mode == evalPrecheck
	}
	return p.ledger == e.ledger && !p.closed
}

func (e *Engine) evaluateAddr(addr netip.Addr, port int, p *Permit, mode evalMode) bool {
	return e.evaluateAddrRequest(addr, port, p, mode, nil)
}

func (e *Engine) evaluateAddrRequest(addr netip.Addr, port int, p *Permit, mode evalMode, request *Request) bool {
	if !addr.IsValid() || port < 1 || port > 65535 || e.ledger == nil {
		return false
	}
	host, at := addr.Unmap().String(), e.now()
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	if !e.validPermit(p, mode) {
		return false
	}
	method, path := "", ""
	if request != nil {
		var err error
		path, err = canonicalPath(request.Path)
		if err != nil {
			return false
		}
		if path != "" {
			method = request.Method
		}
	}
	matches := func(r compiledRule) bool {
		return r.isNumeric && matchesRuleMethod(r, method) && e.matchesLocked(r, host, addr, true, port, path, at)
	}
	for _, lk := range e.locks {
		if matches(lk) {
			return false
		}
	}
	for _, layer := range e.layers {
		allow, denied := e.choose(layer.rules, p, mode, matches)
		if denied != nil {
			return false
		}
		if allow != nil {
			e.useOnce(allow, p, mode)
			return true
		}
	}
	return false
}

// choose checks every deny before reserving an allow. A permanent allow is
// preferred to an owned once grant, then to an unused once grant.
func (e *Engine) choose(rules []compiledRule, p *Permit, mode evalMode, matches func(compiledRule) bool) (allow, denied *compiledRule) {
	priority := 3
	for i := range rules {
		r := &rules[i]
		if !matches(*r) {
			continue
		}
		if !r.allow {
			return nil, r
		}
		if rank, ok := e.oncePriority(*r, p, mode); ok && rank < priority {
			allow, priority = r, rank
		}
	}
	return allow, nil
}

func (e *Engine) nextExpiry(at time.Time) time.Time {
	var next time.Time
	for _, layer := range e.layers {
		for _, r := range layer.rules {
			if r.until.After(at) && (next.IsZero() || r.until.Before(next)) {
				next = r.until
			}
		}
	}
	return next
}
