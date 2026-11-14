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
	var due []RuleExpiryEvent
	for _, layer := range eng.layers {
		for _, r := range layer.rules {
			if !r.until.IsZero() && !at.Before(r.until) {
				due = append(due, RuleExpiryEvent{ID: r.id, GrantID: r.grantID, Until: r.until})
			}
		}
	}
	if len(due) == 0 || s.currentEngine() != eng {
		return
	}
	s.flows.revalidate(eng, s.dialer)
	for _, ended := range due {
		key := onceKey{id: ended.ID, grantID: ended.GrantID, until: canonicalInstant(ended.Until)}
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
	for {
		eng := s.currentEngine()
		at := s.now()
		s.expire(eng, at)
		next := eng.nextExpiry(at)
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
