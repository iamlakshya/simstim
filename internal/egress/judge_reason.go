package egress

import "context"

// sanitizeJudgeReason sees the whole decoded source before the codec removes
// controls or caps it. The same immutable member Source and Egress scrubber
// govern input, the fixed endpoint header and the returned explanation.
func (s *Server) sanitizeJudgeReason(ctx context.Context, raw string) (string, error) {
	if ctx == nil || ctx.Err() != nil || s.policyMu.LockContext(ctx) != nil {
		return "", errJudgeTransport
	}
	defer s.policyMu.Unlock()
	if _, current := s.judgeSourceSnapshotLocked(ctx); !current || ctx.Err() != nil {
		return "", errJudgeTransport
	}
	return cleanAuditText(raw, s.scrubber.snapshot(), maxScrubInputBytes), nil
}
