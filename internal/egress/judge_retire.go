package egress

import "context"

// retireJudgeSourcesLocked joins foreign model calls before a new immutable
// source is installed. The caller holds the existing policyMu. This uses the
// same physical flow records; no second request or secret registry exists.
func (s *Server) retireJudgeSourcesLocked(ctx context.Context, next *memberState) error {
	if err := s.retireSourceExportLocked(ctx, next); err != nil {
		return err
	}
	s.flows.mu.Lock()
	var owners []activeFlow
	for _, flow := range s.flows.flows {
		if flow.judgeSource != nil && flow.judgeSource != next {
			owners = append(owners, flow)
		}
	}
	s.flows.mu.Unlock()
	for _, owner := range owners {
		if owner.cancel == nil || owner.done == nil {
			return memberError("secret_lifetime", "judge source call lacks a physical completion owner; replacement did not occur", "Stop and resume with matching judge lifetime support.")
		}
	}
	if ctx == nil || ctx.Err() != nil {
		return memberError("cancelled", "judge source replacement was cancelled before call closure", "Retry the freshly approved source operation.")
	}
	for _, owner := range owners {
		owner.cancel()
	}
	// Completion removes its flow record and closes done without acquiring
	// policyMu. The model body, response and transport have already finished.
	for _, owner := range owners {
		select {
		case <-owner.done:
		case <-ctx.Done():
			return s.secretJoinUncertain()
		}
	}
	return nil
}
