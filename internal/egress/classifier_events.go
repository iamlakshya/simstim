package egress

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const EventJudgeUnavailable = "judge_unavailable"

func (s *Server) emitJudgeUnavailable(ctx context.Context) error {
	return s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventJudgeUnavailable})
}

func (s *Server) emitClassifierAudit(ctx context.Context, request Request, rule, reason string) error {
	switch rule {
	case "classify:judge-call", "classify:judge-allow", "classify:judge-deny", "classify:judge-unsure":
	default:
		return errJudgeTransport
	}
	entry := s.formatAudit(AuditEntry{Host: request.Host, Port: request.Port, Method: request.Method, Path: request.Path, Mode: "intercept", Rule: rule, Decision: "observe", By: "classifier", Reason: reason})
	return s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventAudit, Audit: &entry})
}

const EventClassifierSession = "classifier_session"

// The optional judge's original session decision nominates only the existing
// private hold. The supervisor must resolve it afresh on this same actor and
// use its real durable writer. This event publishes no target or allowance.
func (s *Server) classifierSessionProposalLocked(binding classifierGrantBinding) (api.NetworkHoldActionArgs, error) {
	if s.currentMembers() != binding.source || s.currentEngine() != binding.engine || s.holds == nil || binding.hold == nil {
		return api.NetworkHoldActionArgs{}, endedPrivateGrant()
	}
	request, revision, expires, sensitive, ok := s.holds.lookup(binding.hold.meta.ID, s.now())
	if !ok || sensitive || !expires.Equal(binding.expires) || request.Host != binding.request.Host || request.Port != binding.request.Port || !api.ValidSessionID(binding.source.metadata.SessionID) {
		return api.NetworkHoldActionArgs{}, endedPrivateGrant()
	}
	return api.NetworkHoldActionArgs{Version: api.NetworkHoldV1, Session: binding.source.metadata.SessionID, Request: binding.hold.meta.ID, Revision: revision, Scope: api.NetworkHoldSession, Maximum: api.NetworkHoldPublicOnly}, nil
}

func (s *Server) emitClassifierSessionProposal(ctx context.Context, args api.NetworkHoldActionArgs) error {
	if !api.ValidNetworkHoldPositiveAction(args) || args.Scope != api.NetworkHoldSession || args.Maximum != api.NetworkHoldPublicOnly {
		return endedPrivateGrant()
	}
	return s.writeControlJSON(ctx, nil, proto.TEvent, CtlEvent{Event: EventClassifierSession, ClassifierSession: &args})
}
