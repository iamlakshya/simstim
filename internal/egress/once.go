package egress

import (
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type onceKey struct {
	tools   string
	id      string
	grantID string
	until   time.Time
}

func canonicalInstant(at time.Time) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	return at.Round(0).UTC()
}

type slotState uint8

const (
	slotAvailable slotState = iota
	slotReserved
	slotCommitted
	slotConsumed
)

type onceSlot struct {
	key   onceKey
	state slotState
	owner *Permit
}

type onceLedger struct {
	mu    sync.Mutex
	slots map[onceKey]*onceSlot
}

func newOnceLedger() *onceLedger { return &onceLedger{slots: map[onceKey]*onceSlot{}} }

func (r compiledRule) instanceKey() onceKey {
	return onceKey{id: r.id, grantID: r.grantID, until: canonicalInstant(r.until), tools: strings.Join(r.tools, "\x00")}
}

// syncPolicy binds a new immutable engine before it is published. Old permits
// retain detached slots, so their cleanup cannot affect a replacement grant.
func (l *onceLedger) syncPolicy(e *Engine) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.syncPolicyLocked(e)
}

// syncPolicyLocked uses the SAME ledger within an existing policy/hold commit.
func (l *onceLedger) syncPolicyLocked(e *Engine) {
	next := make(map[onceKey]*onceSlot)
	bind := func(r *compiledRule) {
		if !r.once || !r.allow {
			r.slot = nil
			return
		}
		key := r.instanceKey()
		slot := next[key]
		if slot == nil {
			slot = l.slots[key]
		}
		if slot == nil {
			slot = &onceSlot{key: key}
		}
		next[key] = slot
		r.slot = slot
	}
	for i := range e.layers {
		for j := range e.layers[i].rules {
			bind(&e.layers[i].rules[j])
		}
	}
	l.slots = next
	e.ledger = l
}

// Permit holds reservations for one flow. It carries no engine or cached
// verdict. Every recheck evaluates the current engine's rules.
type Permit struct {
	ledger            *onceLedger
	owned             map[*onceSlot]struct{}
	sensitiveTarget   bool // source-derived display narrowing only
	classifierGranted bool // provenance of an actually acquired rule
	publicOnly        bool // monotonic restriction under ledger.mu, including live swaps
	committed         bool
	closed            bool
}

// NewPermit starts an uncommitted flow authorisation.
func (e *Engine) NewPermit() *Permit {
	return &Permit{ledger: e.ledger, owned: make(map[*onceSlot]struct{})}
}

// Commit spends the reservations once a TCP connection has been established.
// Failed DNS and TCP attempts close an uncommitted permit and release them.
func (p *Permit) Commit() {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	if p.closed {
		return
	}
	for slot := range p.owned {
		if slot.owner == p && slot.state == slotReserved {
			slot.state = slotCommitted
		}
	}
	p.committed = true
}

// Close releases an unsuccessful attempt, or returns the exact committed
// leases whose flows ended. The caller emits their events after the audit.
func (p *Permit) Close() []OnceEndEvent {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	var ended []OnceEndEvent
	for slot := range p.owned {
		if slot.owner != p {
			continue
		}
		switch slot.state {
		case slotAvailable, slotConsumed:
			// These states hold no live reservation for this permit.
		case slotCommitted:
			slot.state = slotConsumed
			ended = append(ended, OnceEndEvent{ID: slot.key.id, GrantID: slot.key.grantID, Until: slot.key.until})
		case slotReserved:
			slot.state = slotAvailable
		}
		slot.owner = nil
	}
	p.owned = nil
	return ended
}

func (p *Permit) checkpoint() map[*onceSlot]struct{} {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	before := make(map[*onceSlot]struct{}, len(p.owned))
	for slot := range p.owned {
		before[slot] = struct{}{}
	}
	return before
}

func (p *Permit) rollback(before map[*onceSlot]struct{}) {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	for slot := range p.owned {
		if _, keep := before[slot]; keep || slot.owner != p || slot.state != slotReserved {
			continue
		}
		slot.state, slot.owner = slotAvailable, nil
		delete(p.owned, slot)
	}
}

type evalMode uint8

const (
	evalStandalone evalMode = iota
	evalReserve
	evalPrecheck
	evalRevalidate
	evalModelPrecheck
	evalModelReserve
	evalBudgetPrecheck
	evalBudgetReserve
)

// oncePriority runs under ledger.mu. Denies never use or consume a slot.
func (e *Engine) oncePriority(r compiledRule, p *Permit, mode evalMode) (int, bool) {
	if r.uploadBudget && mode != evalBudgetPrecheck && mode != evalBudgetReserve {
		return 0, false
	}
	if e.lockdown && r.allow && r.ref.Kind == api.LayerSession {
		return 0, false
	}
	if !r.once || !r.allow {
		return 0, true
	}
	slot := r.slot
	if slot == nil {
		return 0, false
	}
	if p != nil {
		if _, own := p.owned[slot]; own && slot.owner == p && (slot.state == slotReserved || slot.state == slotCommitted) {
			return 1, true
		}
	}
	if e.ledger.slots[slot.key] != slot {
		return 0, false
	}
	if mode != evalRevalidate && slot.state == slotAvailable && (p == nil || !p.committed || mode == evalModelPrecheck || mode == evalModelReserve || mode == evalBudgetPrecheck || mode == evalBudgetReserve) {
		return 2, true
	}
	return 0, false
}

func (e *Engine) useOnce(r *compiledRule, p *Permit, mode evalMode) {
	if r != nil && r.allow && p != nil && (mode == evalReserve || mode == evalModelReserve || mode == evalBudgetReserve || mode == evalRevalidate) {
		p.sensitiveTarget = p.sensitiveTarget || r.displaySensitive
		p.classifierGranted = p.classifierGranted || r.by == "classifier"
	}
	if r != nil && r.allow && r.publicOnly && p != nil && (mode == evalReserve || mode == evalModelReserve || mode == evalBudgetReserve || mode == evalRevalidate) {
		p.publicOnly = true
	}
	if r == nil || !r.once || !r.allow {
		return
	}
	if mode == evalStandalone {
		r.slot.state, r.slot.owner = slotConsumed, nil
	} else if (mode == evalReserve || mode == evalModelReserve || mode == evalBudgetReserve) && r.slot.state == slotAvailable {
		r.slot.state, r.slot.owner = slotReserved, p
		if p.committed {
			r.slot.state = slotCommitted
		}
		p.owned[r.slot] = struct{}{}
	}
}

func (p *Permit) auditOrigin() (sensitive, classified bool) {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	return p.sensitiveTarget, p.classifierGranted
}
