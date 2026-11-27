package egress

import (
	"context"
	"time"
)

func (s *Server) wakeExpiry() {
	select {
	case s.expiryWake <- struct{}{}:
	default:
	}
}

func (e *Engine) leaseKeys() map[onceKey]struct{} {
	keys := make(map[onceKey]struct{})
	for _, layer := range e.layers {
		for _, r := range layer.rules {
			if !r.until.IsZero() {
				keys[r.instanceKey()] = struct{}{}
			}
		}
	}
	return keys
}

func (s *Server) expire(eng *Engine, at time.Time) {
	type expiry struct {
		event RuleExpiryEvent
		key   onceKey
	}
	var due []expiry
	for _, layer := range eng.layers {
		for _, r := range layer.rules {
			if !r.until.IsZero() && !at.Before(r.until) {
				due = append(due, expiry{event: RuleExpiryEvent{ID: r.id, GrantID: r.grantID, Until: r.until}, key: r.instanceKey()})
			}
		}
	}
	if len(due) == 0 || s.currentEngine() != eng {
		return
	}
	s.flows.revalidate(eng, s.dialer)
	for _, item := range due {
		ended, key := item.event, item.key
		s.mu.Lock()
		if s.engine != eng {
			s.mu.Unlock()
			return
		}
		_, sent := s.expired[key]
		if !sent {
			s.expired[key] = struct{}{}
		}
		s.mu.Unlock()
		if !sent {
			s.emitLeaseEvent(CtlEvent{Event: EventRuleExpiry, RuleExpiry: &ended})
		}
	}
}

// watchExpiry waits for the next lease deadline or policy replacement. There
// is no polling loop when the current policy has no timed grants.
func (s *Server) watchExpiry(ctx context.Context) {
	sentHoldRevision := uint64(1)
	for {
		eng := s.currentEngine()
		at := s.now()
		s.expire(eng, at)
		next := eng.nextExpiry(at)
		if s.holds != nil {
			s.holds.expire(at)
			if s.isInited() {
				revision := s.holds.currentRevision()
				if revision > sentHoldRevision {
					if s.emitNetworkHoldRevision(ctx, revision) != nil {
						return
					}
					sentHoldRevision = revision
				}
			}
			if pending := s.holds.nextExpiry(); !pending.IsZero() && (next.IsZero() || pending.Before(next)) {
				next = pending
			}
		}
		if next.IsZero() {
			select {
			case <-ctx.Done():
				return
			case <-s.expiryWake:
				continue
			}
		}
		timer := time.NewTimer(next.Sub(at))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.expiryWake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
