package egress

import "context"

// CtlUnknownAuditAccepted acknowledges one actual accepted native audit.
const CtlUnknownAuditAccepted = "unknown.audit.accepted"

// UnknownAuditAcceptedArgs names only the current actor's opaque record ID.
type UnknownAuditAcceptedArgs struct {
	Version     int    `json:"v"`
	Destination uint64 `json:"destination"`
}

// UnknownAuditAcceptedResult elects one existing native alert attempt.
// It supplies no network, target, credential or persistent rule authority.
type UnknownAuditAcceptedResult struct {
	First bool `json:"first"`
}

func (s *Server) acceptUnknownAudit(raw []byte) (UnknownAuditAcceptedResult, error) {
	var args UnknownAuditAcceptedArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return UnknownAuditAcceptedResult{}, err
	}
	if args.Version != 1 || args.Destination == 0 || !s.isInited() {
		return UnknownAuditAcceptedResult{}, memberError("bad_request", "invalid accepted unknown audit", "Use the current actor's accepted audit identifier.")
	}
	s.mu.RLock()
	table := s.synth
	s.mu.RUnlock()
	first, known := table.acceptUnknown(args.Destination)
	if !known {
		return UnknownAuditAcceptedResult{}, memberError("unknown_audit_ended", "unknown audit destination is no longer current", "Retry against the current egress actor.")
	}
	return UnknownAuditAcceptedResult{First: first}, nil
}

// The caller owns the existing policy gate. The result is derived solely
// from the actual matcher branch; diagnostic labels do not reserve metadata.
func (s *Server) reserveUnknownDestinationLocked(request Request, decision Decision) Decision {
	if !decision.Allow || !decision.unknownAllowance {
		return decision
	}
	s.mu.RLock()
	table := s.synth
	s.mu.RUnlock()
	if table == nil || !table.reserveUnknown(request.Host) {
		return Decision{Rule: "unknown:resource", unknownPressure: true}
	}
	return decision
}

func (s *Server) precheckUnknownDestination(ctx context.Context, request Request) Decision {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return Decision{Rule: "http:cancelled"}
	}
	defer s.policyMu.Unlock()
	decision := s.currentEngine().evaluate(request, nil, evalPrecheck)
	return s.reserveUnknownDestinationLocked(request, decision)
}

func (s *Server) admitOrdinaryFlow(ctx context.Context, request Request, permit *Permit) Decision {
	if err := s.policyMu.LockContext(ctx); err != nil {
		return Decision{Rule: "http:cancelled"}
	}
	defer s.policyMu.Unlock()
	engine := s.currentEngine()
	decision := s.reserveUnknownDestinationLocked(request, engine.evaluate(request, permit, evalPrecheck))
	if !decision.Allow {
		return decision
	}
	return engine.DecideWithPermit(request, permit)
}

func unknownResourceLine() string {
	return "unknown destination records are full. Try: add an explicit rule for this host or start a fresh sandbox"
}

func (s *Server) ordinaryRefusalLine(decision Decision, host string, port int) string {
	if decision.unknownPressure {
		return unknownResourceLine()
	}
	return RefusalLine(s.sessionRef(), host, port)
}
