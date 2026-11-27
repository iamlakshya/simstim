package egress

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// awaitClassifierUnknown is called after the actual hot path's read-only
// fixed checks, outside policyMu. A call has one budget across the optional
// judge and an early unsure ask; it cannot renew the ordinary hold timer.
func (s *Server) awaitClassifierUnknown(ctx context.Context, request Request, originalHost, via string, conn net.Conn, probe func(*Engine, Request) Decision) Decision {
	if ctx == nil || ctx.Err() != nil || s.holds == nil {
		return Decision{Rule: "classify:unavailable"}
	}
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	abandoned := streamAbandoned(conn)
	if abandoned != nil {
		joined := make(chan struct{})
		go func() {
			defer close(joined)
			select {
			case <-abandoned:
				cancel()
			case <-waitCtx.Done():
			}
		}()
		defer func() { cancel(); <-joined }()
	}
	if s.policyMu.LockContext(waitCtx) != nil {
		return heldDecision(heldRefusalCancelled)
	}
	beforeSource, beforeEngine := s.currentMembers(), s.currentEngine()
	s.policyMu.Unlock()
	outcome := s.runUnknownClassifier(waitCtx, request, via, originalHost)
	if waitCtx.Err() != nil || channelReady(abandoned) {
		return heldDecision(heldRefusalCancelled)
	}
	deadline := outcome.WaitDeadline
	if deadline.IsZero() {
		return outcome.Decision
	}
	var ticket networkHoldTicket
	defer func() {
		if ticket.entry != nil {
			s.holds.detach(ticket)
			s.wakeExpiry()
		}
	}()
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	for {
		if s.policyMu.LockContext(waitCtx) != nil {
			return heldDecision(heldRefusalCancelled)
		}
		if waitCtx.Err() != nil || channelReady(abandoned) {
			s.policyMu.Unlock()
			return heldDecision(heldRefusalCancelled)
		}
		now := s.now()
		s.holds.expire(now)
		engine, source := s.currentEngine(), s.currentMembers()
		stopped := s.holds.refusedCount(now) >= 5 && (engine.unknown == api.UnknownAsk || engine.unknown == api.UnknownClassify)
		candidate := request
		candidate.classifierProbe = candidate.classifierProbe || stopped
		decision := probe(engine, candidate)
		if decision.Allow || !decision.unmatched {
			s.policyMu.Unlock()
			return decision
		}
		if stopped {
			s.policyMu.Unlock()
			return heldDecision(heldRefusalStopped)
		}
		if source == beforeSource && engine == beforeEngine && !outcome.Pending {
			s.policyMu.Unlock()
			if outcome.Decision.Allow {
				// Another physical caller may already have consumed a once
				// grant. Only the current read-only probe may return allow.
				return Decision{Rule: "classify:spent"}
			}
			return outcome.Decision
		}
		if !now.Before(deadline) || outcome.WaitExpired {
			s.policyMu.Unlock()
			return heldDecision(heldRefusalWaiting)
		}
		if source == nil {
			s.policyMu.Unlock()
			return Decision{Rule: "classify:unavailable"}
		}
		settings, err := holdSettings(source.metadata.Policy)
		if err != nil {
			s.policyMu.Unlock()
			return Decision{Rule: "unknown:configuration"}
		}
		action := engine.unknown
		if action == api.UnknownClassify {
			action = settings.unsure
		}
		if action != api.UnknownAsk {
			s.policyMu.Unlock()
			return decision
		}
		if ticket.entry != nil {
			_, reason := s.holds.notice(ticket)
			if reason != "" {
				if reason != "answered" {
					s.policyMu.Unlock()
					return heldDecision(heldRefusalRefused)
				}
				s.holds.detach(ticket)
				ticket = networkHoldTicket{}
			}
		}
		if ticket.entry == nil {
			ticket, _, err = s.holds.attach(request, originalHost, now, settings.open)
			if err != nil {
				s.policyMu.Unlock()
				if errors.Is(err, errHoldPressure) {
					return heldDecision(heldRefusalPressure)
				}
				return Decision{Rule: "classify:unavailable"}
			}
		}
		changed, _ := s.holds.notice(ticket)
		s.policyMu.Unlock()
		s.wakeExpiry()
		select {
		case <-waitCtx.Done():
			return heldDecision(heldRefusalCancelled)
		case <-timer.C:
			return heldDecision(heldRefusalWaiting)
		case <-ticket.entry.done:
		case <-changed:
		}
	}
}
