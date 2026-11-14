package supervisor

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

const egressRecoveryHint = "Check the host's audit storage. Stop and resume the sandbox after fixing the problem."

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// closeEventControl is a fixed lifecycle goroutine, independent of the
// policy worker. Even blocked audit I/O cannot leave networking enabled
// after the reply reader has failed or reached EOF.
func (c *egressCtl) closeEventControl() {
	defer c.s.wg.Done()
	defer close(c.events.controlClosed)
	<-c.done
	c.s.mu.Lock()
	if c.s.m != nil && c.s.eg == c.owner {
		c.s.m.Armed = false
		disarmPublicSecretsLocked(c.s.m)
		c.s.dirty = true
	}
	c.s.mu.Unlock()
	// Closing the owned link does not linger behind a full control window.
	// Egress.Serve stops on that EOF and closes every active flow.
	if c.owner != nil && c.owner.sess != nil {
		_ = c.owner.sess.Close()
	} else {
		_ = c.st.Close()
	}
	c.events.notify()
}

func (c *egressCtl) runEvents(ctx context.Context) {
	defer c.s.wg.Done()
	defer close(c.events.finished)
	var audited uint64
	reported := false
	for {
		audits, fault := c.events.takeAudits()
		for _, entry := range audits {
			c.s.audit.writeNet(entry.entry)
			audited = entry.seq
		}
		if fault && !reported {
			reported = true
			if c.s.log != nil {
				c.s.log.Error("egress audit queue overflow. Networking is stopped", "try", egressRecoveryHint)
			}
			c.s.audit.write("egress_control_failed", map[string]any{"reason": "audit_queue_overflow", "try": egressRecoveryHint})
		}
		closing := channelClosed(c.done)
		var count int
		if c.events.hasEnds() {
			// A live add holds polMu through its RPC, manifest publication
			// and rule-add audit. Never take it in the sole reply reader.
			c.s.polMu.Lock()
			batch := c.events.takeEnds(audited, time.Now(), closing)
			count = len(batch)
			if count > 0 {
				c.applyLeaseEnds(ctx, batch, time.Now())
			}
			c.s.polMu.Unlock()
		}
		if closing && c.events.empty() {
			return
		}
		if len(audits) > 0 || count > 0 {
			continue
		}
		var timer *time.Timer
		var due <-chan time.Time
		if next := c.events.nextDeadline(); !next.IsZero() {
			timer = time.NewTimer(time.Until(next))
			due = timer.C
		}
		select {
		case <-c.events.wake:
		case <-c.done:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// applyLeaseEnds runs under polMu, never under s.mu during merge, RPC,
// persistence or audit I/O. Rules which were refreshed meanwhile survive.
func (c *egressCtl) applyLeaseEnds(ctx context.Context, events []leaseEnd, now time.Time) {
	s := c.s
	s.mu.Lock()
	if s.m == nil {
		s.mu.Unlock()
		return
	}
	if s.eg != c.owner {
		s.mu.Unlock()
		return
	}
	layers := s.m.Layers
	ref := sessionRefOf(s.m)
	ep := s.eg
	next, removed := removeEndedLeases(layers, events, now)
	s.mu.Unlock()
	if len(removed) == 0 {
		return
	}
	pol, cleaned, err := egress.MergeLayers(next)
	if err != nil {
		c.closeOwnedControl()
		c.saveLeaseState(nil, nil, true)
		if s.log != nil {
			s.log.Error("cannot remove an ended network lease. Networking is stopped", "err", err, "try", brand.Name+" policy ls --in <host>/<session>")
		}
		return
	}
	stopped := false
	if !channelClosed(c.done) {
		if perr := c.pushEndedPolicy(ctx, ep, ref, pol); perr != nil {
			// The exact lease must not reappear after an RPC failure. Close
			// networking first, then retain its removal in the manifest.
			c.closeOwnedControl()
			stopped = true
			if s.log != nil {
				s.log.Error("cannot publish an ended lease. Networking is stopped", "err", perr, "try", egressRecoveryHint)
			}
		}
	}
	if !c.saveLeaseState(&pol, cleaned, stopped) {
		return
	}
	for _, rule := range removed {
		s.audit.write("rule_end", map[string]any{"id": rule.ID, "grant_id": rule.GrantID, "until": rule.Until, "once": rule.Once, "reason": leaseEndReason(rule, events, now)})
	}
}

// saveLeaseState publishes only for this control's current owner. Closing
// its link may wake other lifecycle goroutines, so disarming belongs in the
// same snapshot as the lease removal rather than a later asynchronous save.
func (c *egressCtl) saveLeaseState(pol *api.Policy, layers []api.Layer, stopped bool) bool {
	s := c.s
	s.mu.Lock()
	if s.m == nil || s.eg != c.owner {
		s.mu.Unlock()
		return false
	}
	if pol != nil {
		s.m.Policy, s.m.Layers = pol, layers
	}
	// A control EOF or child exit can happen while this worker waits for
	// s.mu. Observe both here so the saved state reflects that stop too.
	if stopped || channelClosed(c.done) || (c.owner != nil && c.owner.dead.Load()) {
		s.m.Armed = false
		disarmPublicSecretsLocked(s.m)
		s.dirty = true
	}
	snap := s.snapshot()
	s.mu.Unlock()
	_ = s.save(&snap, false)
	return true
}

func (c *egressCtl) pushEndedPolicy(ctx context.Context, ep *egressProc, ref string, pol api.Policy) *proto.Error {
	if ep == nil || ep.dead.Load() || ep.ctl == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ep.ctl.request(ctx, egress.CtlPolicyUpdate, egress.PolicyUpdateArgs{SessionRef: ref, Policy: pol}, nil); err != nil {
		return toProtoErr(err)
	}
	return nil
}

func leaseEndReason(rule api.Rule, events []leaseEnd, now time.Time) string {
	for _, ev := range events {
		if ev.expiry && ev.matches(rule, now) {
			return "expired"
		}
	}
	return "once"
}
