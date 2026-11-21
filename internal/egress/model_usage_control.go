package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const CtlModelUsage = "model.usage"
const EventModelBudget = "model_budget"

func (s *Server) modelUsageSnapshot() api.ModelUsageV1 {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	return s.usage.snapshotWithSourceV1(s.scrubber.snapshot())
}

func (s *Server) emitModelBudget(ctx context.Context, refusal *ModelBudgetRefusalV1) {
	if refusal != nil && refusal.Alert {
		if err := s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventModelBudget}); err != nil {
			s.usage.mu.Lock()
			s.usage.alerted = false
			s.usage.mu.Unlock()
		}
	}
}
