package supervisor

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/alerts"
	"github.com/iamlakshya/simstim/internal/api"
)

// These are bounded pending jobs inside the existing event queue. An opaque
// ID or a coalesced revision is never a target, grant or publication receipt.
func (q *egressEvents) enqueueClassifierSessionLocked(args api.NetworkHoldActionArgs) bool {
	if !api.ValidNetworkHoldPositiveAction(args) || args.Scope != api.NetworkHoldSession || args.Maximum != api.NetworkHoldPublicOnly {
		return false
	}
	if q.classifierSessions == nil {
		q.classifierSessions = make(map[string]api.NetworkHoldActionArgs)
	}
	if previous, exists := q.classifierSessions[args.Request]; exists {
		if previous.Session != args.Session {
			return false
		}
		if args.Revision > previous.Revision {
			q.classifierSessions[args.Request] = args
		}
		q.notify()
		return true
	}
	if len(q.classifierSessions) >= api.NetworkHoldCapacity {
		// Optional publication pressure leaves this question pending. It
		// cannot claim an allowance or stop unrelated networking.
		return true
	}
	q.classifierSessions[args.Request] = args
	q.classifierOrder = append(q.classifierOrder, args.Request)
	q.notify()
	return true
}

func (q *egressEvents) takeClassifierSessions() []api.NetworkHoldActionArgs {
	q.mu.Lock()
	defer q.mu.Unlock()
	batch := make([]api.NetworkHoldActionArgs, 0, len(q.classifierOrder))
	for _, id := range q.classifierOrder {
		batch = append(batch, q.classifierSessions[id])
	}
	clear(q.classifierSessions)
	q.classifierOrder = nil
	return batch
}

func (q *egressEvents) takeJudgeUnavailable() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.judgeUnavailable
	q.judgeUnavailable = false
	return pending
}

func (c *egressCtl) applyClassifierSession(ctx context.Context, args api.NetworkHoldActionArgs) {
	s := c.s
	if !api.ValidNetworkHoldPositiveAction(args) || args.Scope != api.NetworkHoldSession || args.Maximum != api.NetworkHoldPublicOnly || args.Session != s.id || s.owner() != s || !s.sameNetworkActor(c.owner) {
		return
	}
	// This is the same private current-actor resolve, source export and durable
	// session writer used for a user's public session answer. It accepts no
	// incoming Target and acknowledges no grant from this event itself.
	owned, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = s.resolveNetworkHoldFromActorV1(owned, args, c.owner)
}

func (c *egressCtl) raiseJudgeUnavailable() {
	s := c.s
	if !s.sameNetworkActor(c.owner) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		s.raise(s.alertEvent(alerts.JudgeUnavailable))
	}
}

func (q *egressEvents) takeModelBudget() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.modelBudget
	q.modelBudget = false
	return pending
}
func (c *egressCtl) raiseModelBudget() {
	s := c.s
	if !s.sameNetworkActor(c.owner) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m != nil {
		s.raise(s.alertEvent(alerts.ModelBudget))
	}
}
