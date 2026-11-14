package egress

import "net/http"

type httpRefusalKind uint8

const (
	httpRefusalNone httpRefusalKind = iota
	httpRefusalReadOnly
	httpRefusalUnavailable
)

// evaluateHTTP retains the sole policy matcher and adds the static service
// barrier to its typed result. DNS witnesses, CONNECT eligibility and actual
// requests must agree on this same effective method/path decision.
func (e *Engine) evaluateHTTP(request Request, permit *Permit, mode evalMode) Decision {
	decision := e.evaluate(request, permit, mode)
	if !decision.Allow {
		return decision
	}
	kind, spliceOnly, err := httpClass(request.Host)
	if err != nil || spliceOnly {
		decision.Allow, decision.Rule = false, "class:unavailable"
		decision.httpRefusal = httpRefusalUnavailable
		decision.unmatched = false
		return decision
	}
	if (kind == "registry" || kind == "object") && request.Method != http.MethodGet && request.Method != http.MethodHead && !explicitPathAllowance(decision) {
		decision.Allow, decision.Rule = false, "class:read_only"
		decision.httpRefusal = httpRefusalReadOnly
		decision.unmatched = false
	}
	return decision
}

func (e *Engine) requiresHTTPWitness(host string, port int) bool {
	kind, spliceOnly, err := httpClass(host)
	return err != nil || !spliceOnly && (kind != "" || e.hasInspectionRule(host, port))
}

func (e *Engine) revalidateFlow(request Request, permit *Permit) Decision {
	if request.Path != "" {
		return e.evaluateModelNetwork(request, permit, evalRevalidate)
	}
	return e.Revalidate(request, permit)
}
