package egress

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
)

func applyUsageAudit(entry *AuditEntry, completion ModelUsageCompletionV1) {
	if completion.Counts.Requests == 0 {
		return
	}
	entry.Usage = "reported"
	if completion.Estimated {
		entry.Usage = "estimate"
	}
	counts := completion.Counts
	entry.ModelUsage = &counts
	entry.UsageSaturated = completion.Saturated
}
func (s *Server) emitUsageAudit(ctx context.Context, entry AuditEntry) {
	if ctx == nil {
		return
	}
	// The completed/cut counters have no credential-bearing payload. Their
	// append belongs to this same native control lifetime, including when
	// source revocation cancelled the former origin request.
	owned, cancel := s.controlMutationContext()
	defer cancel()
	formatted := s.formatAudit(entry)
	_ = s.writeControlJSON(owned, nil, proto.TEvent, CtlEvent{Event: EventAudit, Audit: &formatted})
}
