package egress

func deferServiceRequest(request Request) bool {
	barriers, err := serviceBarriersFor(request)
	return err != nil || len(barriers) != 0
}

// Required endpoint authority and ordinary networking use ONE locked matcher
// and original permit. Acquire the required path lease first so networking
// can reuse it instead of spending an unrelated host-once grant. Every
// precheck, deny/lock and body/shape check precedes mutation and origin IO.
func (e *Engine) admitServiceRequest(request Request, permit *Permit, needPath bool, mode evalMode) Decision {
	canonical, valid := canonicalModelRequest(request)
	if !valid || e.ledger == nil {
		return Decision{Rule: "service:invalid"}
	}
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	if !e.validPermit(permit, mode) || mode != evalReserve && mode != evalPrecheck && mode != evalRevalidate {
		return Decision{Rule: "service:permit"}
	}
	if mode == evalReserve && permit.committed {
		return Decision{Rule: "service:permit"}
	}
	if canonical.Now.IsZero() {
		canonical.Now = e.now()
	}
	check := evalPrecheck
	if mode == evalRevalidate {
		check = evalRevalidate
	}
	network := e.evaluateHTTPLocked(canonical, permit, check)
	if !network.Allow {
		return network
	}
	path := canonical
	path.servicePathOnly = true
	if needPath {
		decision := e.evaluateHTTPLocked(path, permit, check)
		if !decision.Allow || !explicitPathAllowance(decision) {
			return Decision{Rule: "service:path"}
		}
	}
	if mode != evalReserve {
		return network
	}
	if needPath {
		decision := e.evaluateHTTPLocked(path, permit, evalReserve)
		if !decision.Allow {
			return decision
		}
	}
	return e.evaluateHTTPLocked(canonical, permit, evalReserve)
}
