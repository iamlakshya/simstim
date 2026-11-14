package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Server) formatAudit(e AuditEntry) AuditEntry {
	return FormatAudit(e, s.scrubber, s.now())
}

func (s *Server) emitAudit(e AuditEntry) {
	_ = s.writeControlJSON(context.Background(), nil, proto.TEvent, CtlEvent{Event: EventAudit, Audit: &e})
}

func (s *Server) emitOnceEnd(ended OnceEndEvent) {
	s.emitLeaseEvent(CtlEvent{Event: EventOnceEnd, OnceEnd: &ended})
}

func (s *Server) emitLeaseEvent(event CtlEvent) {
	_ = s.writeControlJSON(context.Background(), nil, proto.TEvent, event)
}

func (s *Server) emitMintedState(ctx context.Context, event MintedStateEvent) error {
	return s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventSecretMinted, Minted: &event})
}
