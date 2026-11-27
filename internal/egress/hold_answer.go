package egress

import (
	"net/netip"

	"github.com/iamlakshya/simstim/internal/api"
)

// Answer resolves only the current private holder on this owned control
// actor. The Session field retains the original selected member for its
// supervisor; it cannot select a different Egress actor or target.
func (s *Server) answerNetworkHold(raw []byte) (api.NetworkHoldAnswerResult, error) {
	var args api.NetworkHoldActionArgs
	if err := decodeControlArgs(raw, &args); err != nil {
		return api.NetworkHoldAnswerResult{}, err
	}
	if !api.ValidNetworkHoldPositiveAction(args) || args.Scope != api.NetworkHoldOnce && args.Scope != api.NetworkHoldSession {
		return api.NetworkHoldAnswerResult{}, registryIntentError()
	}
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return api.NetworkHoldAnswerResult{}, err
	}
	defer s.policyMu.Unlock()
	if s.currentMembers() == nil || s.holds == nil {
		return api.NetworkHoldAnswerResult{}, endedPrivateGrant()
	}
	h := s.holds
	h.mu.Lock()
	h.expireLocked(s.now())
	entry := h.byID[args.Request]
	if entry == nil || entry.meta.Revision != args.Revision || !s.now().Before(entry.meta.Expires) {
		h.mu.Unlock()
		return api.NetworkHoldAnswerResult{}, endedPrivateGrant()
	}
	request := entry.request
	h.mu.Unlock()
	publicOnly := args.Maximum == api.NetworkHoldPublicOnly
	if address, err := netip.ParseAddr(request.Host); err == nil {
		publicOnly = true
		if _, allowed := s.dialer.CheckAddr(address, request.Port, nil); !allowed {
			return api.NetworkHoldAnswerResult{}, endedPrivateGrant()
		}
	}
	installed, err := s.installMemoryGrantLocked(ctx, entry, args.Scope, publicOnly, "live", request)
	if err != nil {
		return api.NetworkHoldAnswerResult{}, err
	}
	return api.NetworkHoldAnswerResult{Version: api.NetworkHoldV1, Scope: args.Scope, RuleIDs: []string{installed.ID}, MemoryOnly: true}, nil
}
