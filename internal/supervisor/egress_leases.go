package supervisor

import (
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

type leaseIdentity struct {
	id      string
	grantID string
	until   time.Time
}

func canonicalLeaseTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return t.Round(0).UTC()
}

func ruleLeaseIdentity(r api.Rule) leaseIdentity {
	return leaseIdentity{r.ID, r.GrantID, canonicalLeaseTime(r.Until)}
}

type leaseEnd struct {
	identity leaseIdentity
	expiry   bool
	// auditSeq is the last audit frame received before this event.
	auditSeq uint64
}

func leaseEndOf(ev egress.CtlEvent) (leaseEnd, bool) {
	var ended egress.OnceEndEvent
	expiry := false
	switch {
	case ev.Event == egress.EventOnceEnd && ev.OnceEnd != nil:
		ended = *ev.OnceEnd
	case ev.Event == egress.EventRuleExpiry && ev.RuleExpiry != nil:
		ended = egress.OnceEndEvent(*ev.RuleExpiry)
		expiry = true
	default:
		return leaseEnd{}, false
	}
	if ended.ID == "" {
		return leaseEnd{}, false
	}
	return leaseEnd{identity: leaseIdentity{ended.ID, ended.GrantID, canonicalLeaseTime(ended.Until)}, expiry: expiry}, true
}

func (ev leaseEnd) matches(r api.Rule, now time.Time) bool {
	if ruleLeaseIdentity(r) != ev.identity {
		return false
	}
	if ev.expiry {
		return !r.Until.IsZero() && !now.Before(r.Until)
	}
	return r.Once
}

// removeEndedLeases clones every layer and rule slice. A stale event must
// never alter a fresh grant or leave a partial change behind on merge failure.
func removeEndedLeases(layers []api.Layer, events []leaseEnd, now time.Time) ([]api.Layer, []api.Rule) {
	next := slices.Clone(layers)
	var removed []api.Rule
	for i, layer := range layers {
		next[i].Rules = make([]api.Rule, 0, len(layer.Rules))
		if layer.Lock != nil {
			lock := *layer.Lock
			lock.Deny = slices.Clone(layer.Lock.Deny)
			next[i].Lock = &lock
		}
		for _, rule := range layer.Rules {
			ended := slices.ContainsFunc(events, func(ev leaseEnd) bool { return ev.matches(rule, now) })
			if ended {
				removed = append(removed, rule)
			} else {
				next[i].Rules = append(next[i].Rules, rule)
			}
		}
	}
	return next, removed
}
