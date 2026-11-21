package egress

import "github.com/iamlakshya/simstim/internal/api"

// The existing publication owner holds policyMu. Pending bookkeeping changes
// neither reserve a grant nor perform DNS/IO. Zero waiters still get reconciled.
func (s *Server) reconcileNetworkHoldsLocked() {
	if s.holds == nil {
		return
	}
	h, engine := s.holds, s.currentEngine()
	h.mu.Lock()
	tickets := make([]networkHoldTicket, 0, len(h.entries))
	for _, entry := range h.entries {
		tickets = append(tickets, networkHoldTicket{entry: entry})
	}
	h.mu.Unlock()
	for _, ticket := range tickets {
		h.mu.Lock()
		request := ticket.entry.request
		current := h.byID[ticket.entry.meta.ID] == ticket.entry
		h.mu.Unlock()
		if !current {
			continue
		}
		if request.uploadBudgetOnly {
			s.reconcileUploadHoldLocked(ticket, request)
			continue
		}
		decision := engine.evaluate(request, nil, evalPrecheck)
		if decision.Allow || request.Path == "" && engine.permitsIntercept(request.Host, request.Port) {
			h.answer(ticket, s.now())
			continue
		}
		if !decision.unmatched || engine.unknown != api.UnknownAsk && engine.unknown != api.UnknownClassify {
			h.refuse(ticket.entry.meta.ID, s.now())
		}
	}
}
