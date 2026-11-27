package client

import (
	"context"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type hostTerminalOutcome struct {
	mu     sync.Mutex
	result HostStateRemovalResult
}

type hostTerminalPlan struct {
	frame      hostTerminalFrame
	op         *heldLock
	outcome    *hostTerminalOutcome
	checkpoint cachefs.Reference
	authority  func(context.Context) error
}

// FinalResult returns the concrete cleanup outcome after the outer verified
// callback and its terminal epilogue have returned. During that callback it
// still reports retained domains. This method performs no I/O or mutation.
func (r HostStateRemovalResult) FinalResult() HostStateRemovalResult {
	if r.outcome == nil {
		return cloneHostStateRemovalResult(r)
	}
	r.outcome.mu.Lock()
	defer r.outcome.mu.Unlock()
	return cloneHostStateRemovalResult(r.outcome.result)
}

func cloneHostStateRemovalResult(r HostStateRemovalResult) HostStateRemovalResult {
	r.Removed = slices.Clone(r.Removed)
	r.Retained = slices.Clone(r.Retained)
	r.Preserved = slices.Clone(r.Preserved)
	r.Notes = slices.Clone(r.Notes)
	r.outcome = nil
	return r
}

func (p *hostTerminalPlan) resolve(result HostStateRemovalResult) {
	p.outcome.mu.Lock()
	p.outcome.result = cloneHostStateRemovalResult(result)
	p.outcome.mu.Unlock()
}

func (s *hostForgetMembership) queueTerminal(plan *hostTerminalPlan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active.Load() || s.terminal != nil {
		return hostStateRemovalError("this verified scope already ended or queued terminal cleanup")
	}
	s.terminal = plan
	return nil
}

// finishTerminal runs only after scope.stop has joined every nested worker.
// Its caller continues holding membership and the actual exclusive OP gate.
func (s *hostForgetMembership) finishTerminal(ctx context.Context, current *hostMembershipSnapshot, callbackErr error) error {
	s.mu.Lock()
	plan := s.terminal
	s.mu.Unlock()
	if plan == nil {
		return callbackErr
	}
	if callbackErr != nil {
		result := (HostStateRemovalResult{outcome: plan.outcome}).FinalResult()
		result.Notes = []string{"The outer local callback failed before the destructive retirement epilogue."}
		plan.resolve(result)
		return callbackErr
	}
	if err := current.check(ctx); err != nil {
		return err
	}
	plan.authority = current.check
	return plan.finalise(ctx, s.generation)
}
