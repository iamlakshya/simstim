package egress

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type networkHoldSettings struct {
	wait, open time.Duration
	unsure     api.Unknown
}

func holdSettings(policy api.Policy) (networkHoldSettings, error) {
	settings := networkHoldSettings{wait: 20 * time.Second, open: 5 * time.Minute, unsure: api.UnknownAsk}
	for _, field := range []struct {
		name  string
		value *string
		out   *time.Duration
	}{{"network.hold", policy.Network.Hold, &settings.wait}, {"network.ask_open", policy.Network.AskOpen, &settings.open}} {
		if field.value == nil {
			continue
		}
		parsed, err := time.ParseDuration(*field.value)
		if err != nil || parsed <= 0 {
			return settings, badLayerValue(field.name, "needs a positive duration")
		}
		*field.out = parsed
	}
	if value := policy.Classifier.Unsure; value != nil {
		if *value != string(api.UnknownAsk) && *value != string(api.UnknownDeny) {
			return settings, badLayerValue("classifier.unsure", "needs ask or deny")
		}
		settings.unsure = api.Unknown(*value)
	}
	return settings, nil
}

func streamAbandoned(conn net.Conn) <-chan struct{} {
	if stream, ok := conn.(interface{ Done() <-chan struct{} }); ok {
		return stream.Done()
	}
	return nil
}

func channelReady(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func heldDecision(reason heldRefusalReason) Decision {
	rule := "unknown:cancelled"
	switch reason {
	case heldRefusalWaiting:
		rule = "unknown:waiting"
	case heldRefusalPressure:
		rule = "unknown:pressure"
	case heldRefusalStopped:
		rule = "unknown:stopped"
	case heldRefusalRefused:
		rule = "unknown:refused"
	}
	return Decision{Rule: rule, pendingRefusal: reason}
}

// awaitUnknown is the actual no-I/O wait between ordinary policy prechecks.
// The same policy gate excludes mutation only during each check/attachment;
// no policy, ledger or held-state lock remains held while the flow waits.
// The probe must use the current engine in read-only mode, never reserve.
func (s *Server) awaitUnknown(ctx context.Context, request Request, originalHost, via string, conn net.Conn, probe func(*Engine, Request) Decision) Decision {
	if ctx == nil {
		return Decision{Rule: "unknown:unavailable"}
	}
	if s.holds == nil {
		decision := probe(s.currentEngine(), request)
		if decision.Allow || !decision.unmatched {
			return decision
		}
		return Decision{Rule: "unknown:unavailable"}
	}
	var ticket networkHoldTicket
	var timer, budgetTimer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
		if budgetTimer != nil {
			budgetTimer.Stop()
		}
		if ticket.entry != nil {
			s.holds.detach(ticket)
			s.wakeExpiry()
		}
	}()
	abandoned := streamAbandoned(conn)
	for {
		if ctx.Err() != nil || channelReady(abandoned) {
			return heldDecision(heldRefusalCancelled)
		}
		if s.policyMu.LockContext(ctx) != nil {
			return heldDecision(heldRefusalCancelled)
		}
		if ctx.Err() != nil || channelReady(abandoned) {
			s.policyMu.Unlock()
			return heldDecision(heldRefusalCancelled)
		}
		now := s.now()
		s.holds.expire(now)
		if ticket.entry != nil {
			_, reason := s.holds.notice(ticket)
			if reason != "" {
				if reason != "answered" {
					s.policyMu.Unlock()
					return heldDecision(heldRefusalRefused)
				}
				ticket = networkHoldTicket{}
			}
		}
		engine := s.currentEngine()
		stopped := !request.uploadBudgetOnly && s.holds.refusedCount(now) >= 5 && (engine.unknown == api.UnknownAsk || engine.unknown == api.UnknownClassify)
		candidate := request
		candidate.classifierProbe = candidate.classifierProbe || stopped
		decision := probe(engine, candidate)
		if stopped && decision.unmatched {
			s.policyMu.Unlock()
			return heldDecision(heldRefusalStopped)
		}
		if decision.Allow || !decision.unmatched {
			if decision.classifiedRefusal {
				s.holds.noteClassifierRefusal(request, now)
			}
			if ticket.entry != nil {
				s.holds.answer(ticket, now)
			}
			s.policyMu.Unlock()
			s.wakeExpiry()
			return decision
		}
		if engine.unknown == api.UnknownClassify && !request.uploadBudgetOnly {
			s.policyMu.Unlock()
			return s.awaitClassifierUnknown(ctx, request, originalHost, via, conn, probe)
		}
		state := s.currentMembers()
		if state == nil {
			s.policyMu.Unlock()
			return Decision{Rule: "unknown:unavailable"}
		}
		settings, err := holdSettings(state.metadata.Policy)
		if err != nil {
			s.policyMu.Unlock()
			return Decision{Rule: "unknown:configuration"}
		}
		action := engine.unknown
		if request.uploadBudgetOnly {
			action = api.UnknownAsk
		}
		if action == api.UnknownClassify && !request.uploadBudgetOnly {
			action = settings.unsure
		}
		if action != api.UnknownAsk {
			if engine.unknown == api.UnknownClassify {
				s.holds.noteClassifierRefusal(request, now)
			}
			if ticket.entry != nil {
				s.holds.answer(ticket, now)
			}
			s.policyMu.Unlock()
			s.wakeExpiry()
			return decision
		}
		if ticket.entry == nil {
			ticket, _, err = s.holds.attach(request, originalHost, now, settings.open)
			if err != nil {
				s.policyMu.Unlock()
				s.wakeExpiry()
				if errors.Is(err, errHoldPressure) {
					return heldDecision(heldRefusalPressure)
				}
				return Decision{Rule: "unknown:unavailable"}
			}
		}
		changed, _ := s.holds.notice(ticket)
		if timer == nil {
			wait := settings.wait
			if via == "transparent" {
				wait = min(wait, 3*time.Second)
			}
			timer = time.NewTimer(wait)
		}
		var budgetChanged <-chan struct{}
		var budgetDue <-chan time.Time
		if request.uploadBudgetOnly {
			limit, _ := uploadLimit(state.metadata.Policy)
			var due time.Time
			var ready bool
			ready, budgetChanged, due = s.uploads.available(request.Host, limit, now)
			if ready {
				s.policyMu.Unlock()
				continue
			}
			if budgetTimer != nil {
				budgetTimer.Stop()
			}
			if !due.IsZero() {
				budgetTimer = time.NewTimer(max(0, due.Sub(now)))
				budgetDue = budgetTimer.C
			}
		}
		s.policyMu.Unlock()
		s.wakeExpiry()
		select {
		case <-ctx.Done():
			return heldDecision(heldRefusalCancelled)
		case <-abandoned:
			return heldDecision(heldRefusalCancelled)
		case <-timer.C:
			return heldDecision(heldRefusalWaiting)
		case <-ticket.entry.done:
		case <-changed:
		case <-budgetChanged:
		case <-budgetDue:
		}
	}
}
