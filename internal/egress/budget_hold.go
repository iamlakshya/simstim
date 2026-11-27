package egress

import (
	"errors"
	"fmt"
)

var errUploadBudget = errors.New("upload budget reached. Try: answer once or raise network.upload_budget through the current policy")

func uploadBudgetScopeError() error {
	return memberError("upload_budget_scope", "an upload-budget question accepts once or refuse, without saving a network allowance", "Choose once for the waiting flow, or change network.upload_budget through the normal policy writer.")
}

func (c *uploadBudgetConn) awaitBudget() error {
	s := c.server
	candidate := c.request
	candidate.uploadBudgetOnly = true
	candidate.budgetPermit = c.permit
	original := originalSecretDestination(c.ctx, c.request.Host)
	decision := s.awaitUnknown(c.ctx, candidate, original, c.request.via, nil, func(engine *Engine, request Request) Decision {
		if !s.flowAllowed(engine, activeFlow{request: c.request, addr: c.addr, permit: c.permit}) {
			return Decision{Rule: "budget:revoked"}
		}
		state := s.currentMembers()
		if state == nil {
			return Decision{Rule: "budget:unavailable"}
		}
		limit, err := uploadLimit(state.metadata.Policy)
		if err != nil {
			return Decision{Rule: "budget:configuration"}
		}
		if available, _, _ := s.uploads.available(request.Host, limit, s.now()); available {
			return Decision{Allow: true, Rule: "budget:available"}
		}
		return engine.evaluate(request, c.permit, evalBudgetPrecheck)
	})
	if !decision.Allow {
		return fmt.Errorf("%w: %s", errUploadBudget, s.networkRefusalLine(decision, c.request, original))
	}
	return nil
}

// Called by the same publication owner. An ordinary net allowance never
// answers a budget question; a current larger byte setting or hour can do so.
func (s *Server) reconcileUploadHoldLocked(ticket networkHoldTicket, request Request) {
	state := s.currentMembers()
	if state == nil || s.uploads == nil {
		return
	}
	limit, err := uploadLimit(state.metadata.Policy)
	if err != nil {
		return
	}
	if available, _, _ := s.uploads.available(request.Host, limit, s.now()); available {
		s.holds.answer(ticket, s.now())
	}
}
