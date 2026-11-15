package egress

import (
	"net"
	"net/http"
	"strconv"
)

func originalRequestSecretView(req *http.Request) requestSecretRawView {
	return requestSecretRawView{Target: req.RequestURI, Authority: req.Host, Headers: req.Header.Clone()}
}

// One registry ownership interval covers pure tripwires, the canonical policy
// precheck, credential pin/401, the actual reservation and resource tagging.
// It ends before any request-body read, DNS, real-header write or origin IO.
func (s *Server) admitSecretHTTPRequest(scope *originRequest, req *http.Request, original requestSecretRawView, hdr StreamHeader, scheme string) (requestSecretPlan, Decision, *requestSecretFailure) {
	if err := s.policyMu.LockContext(scope.ctx); err != nil {
		return requestSecretPlan{}, Decision{}, requestSecretRefusal(http.StatusForbidden, "cancelled")
	}
	defer s.policyMu.Unlock()
	snapshot := s.requestSecretViewLocked()
	if failure := checkRequestSecretTripwires(snapshot, req, original, scope.request, scheme); failure != nil {
		return requestSecretPlan{}, Decision{}, failure
	}
	engine := s.currentEngine()
	decision := engine.evaluateModelNetwork(scope.request, scope.permit, evalPrecheck)
	if !decision.Allow {
		return requestSecretPlan{}, decision, nil
	}
	plan, failure := prepareRequestSecrets(snapshot, req, original, scope.request, scheme)
	if failure != nil {
		return requestSecretPlan{}, Decision{}, failure
	}
	if snapshot != nil {
		if err := s.markSecretCarrierInspectedLocked(req.Context(), hdr); err != nil {
			return requestSecretPlan{}, Decision{}, requestSecretRefusal(http.StatusForbidden, "secret_lifetime")
		}
	}
	kind, _, _ := httpClass(scope.request.Host)
	mode := evalReserve
	_, push := gitPushHTTPRoute(scope.request)
	if kind == "model" || push {
		mode = evalPrecheck
	} // The complete body is checked before any model-host lease is reserved.
	decision = engine.evaluateModelNetwork(scope.request, scope.permit, mode)
	scope.mu.Lock()
	scope.reserved = mode == evalReserve
	scope.mu.Unlock()
	if !decision.Allow {
		return requestSecretPlan{}, decision, nil
	}
	if !scope.track() {
		return requestSecretPlan{}, Decision{}, requestSecretRefusal(http.StatusForbidden, "cancelled")
	}
	if snapshot != nil {
		authority := net.JoinHostPort(scope.request.Host, strconv.Itoa(scope.request.Port))
		if err := scope.tagSecretNamesLocked(s.boundSecretNamesLocked(authority)); err != nil {
			return requestSecretPlan{}, Decision{}, requestSecretRefusal(http.StatusForbidden, "secret_lifetime")
		}
	}
	return plan, decision, nil
}

// Only an admitted plan reaches ReverseProxy's actual outbound header copy.
// Routing, the original request and the guest environment are never rewritten.
func (plan requestSecretPlan) applyHeaders(headers http.Header) {
	for _, replacement := range plan.replacements {
		for _, original := range replacement.removeKeys {
			delete(headers, original)
		}
		headers.Set(replacement.header, string(replacement.value))
	}
}
