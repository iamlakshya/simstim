package supervisor

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

const (
	maxQueuedNetAudits  = 128
	maxQueuedAuditBytes = 1 << 20
)

type queuedNetAudit struct {
	entry egress.AuditEntry
	seq   uint64
	bytes int
}

type leaseEndKey struct {
	identity leaseIdentity
	expiry   bool
}

// egressEvents uses a leaf mutex. No caller holds it during disk or RPC I/O.
// Expected identities come from current and in-flight policies, not from the
// still-old manifest seen before a policy-update reply has been published.
type egressEvents struct {
	mu                                     sync.Mutex
	wake                                   chan struct{}
	finished                               chan struct{}
	controlClosed                          chan struct{}
	audits                                 []queuedNetAudit
	auditBytes                             int
	auditSeq                               uint64
	emergency                              *queuedNetAudit
	fault                                  bool
	current                                map[leaseIdentity]struct{}
	pending                                map[leaseIdentity]struct{}
	known                                  map[leaseIdentity]struct{}
	ends                                   map[leaseEndKey]leaseEnd
	order                                  []leaseEndKey
	networkRevision                        uint64
	classifierSessions                     map[string]api.NetworkHoldActionArgs
	classifierOrder                        []string
	judgeUnavailable, judgeUnavailableSeen bool
	modelBudget, modelBudgetSeen           bool
}

func newEgressEvents() *egressEvents {
	return &egressEvents{wake: make(chan struct{}, 1), finished: make(chan struct{}), controlClosed: make(chan struct{}),
		known: make(map[leaseIdentity]struct{}), ends: make(map[leaseEndKey]leaseEnd)}
}

func (q *egressEvents) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func policyLeaseIdentities(pol api.Policy) map[leaseIdentity]struct{} {
	out := make(map[leaseIdentity]struct{})
	for _, r := range pol.Rules {
		if r.Once || !r.Until.IsZero() {
			out[ruleLeaseIdentity(r)] = struct{}{}
		}
	}
	return out
}

// preparePolicy is called by serial policy requests before writing the RPC.
// The reply reader can therefore recognise an event from the new generation
// even while its caller still holds polMu and has not published the manifest.
func (q *egressEvents) preparePolicy(op string, raw []byte) func(bool) {
	var pol api.Policy
	switch op {
	case egress.CtlInit:
		var a egress.InitArgs
		if json.Unmarshal(raw, &a) != nil {
			return func(bool) {}
		}
		pol = a.Policy
	case egress.CtlBoundInit:
		var a egress.BoundInitArgs
		if json.Unmarshal(raw, &a) != nil {
			return func(bool) {}
		}
		pol = a.Initial.Policy
	case egress.CtlPolicyUpdate:
		var a egress.PolicyUpdateArgs
		if json.Unmarshal(raw, &a) != nil {
			return func(bool) {}
		}
		pol = a.Policy
	default:
		return func(bool) {}
	}
	q.mu.Lock()
	q.pending = policyLeaseIdentities(pol)
	q.refreshKnownLocked()
	q.mu.Unlock()
	return func(accepted bool) {
		q.mu.Lock()
		if accepted {
			q.current = q.pending
		}
		q.pending = nil
		q.refreshKnownLocked()
		q.mu.Unlock()
		q.notify()
	}
}

func (q *egressEvents) refreshKnownLocked() {
	q.known = make(map[leaseIdentity]struct{}, len(q.current)+len(q.pending))
	for k := range q.current {
		q.known[k] = struct{}{}
	}
	for k := range q.pending {
		q.known[k] = struct{}{}
	}
	// Retired generations cannot match the next manifest. Pruning them keeps
	// idle expiry churn bounded by the current and single in-flight policy.
	kept := q.order[:0]
	for _, k := range q.order {
		if _, ok := q.known[k.identity]; ok {
			kept = append(kept, k)
		} else {
			delete(q.ends, k)
		}
	}
	q.order = kept
}

// enqueue only stores bounded data. The final received audit has one bounded
// emergency slot on overflow, then the control stream fails closed.
func (q *egressEvents) enqueue(ev egress.CtlEvent, bytes int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.fault {
		return false
	}
	if ev.Event == egress.EventClassifierSession {
		return ev.ClassifierSession != nil && q.enqueueClassifierSessionLocked(*ev.ClassifierSession)
	}
	if ev.Event == egress.EventModelBudget {
		if !q.modelBudgetSeen {
			q.modelBudget, q.modelBudgetSeen = true, true
			q.notify()
		}
		return true
	}
	if ev.Event == egress.EventJudgeUnavailable {
		if !q.judgeUnavailableSeen {
			q.judgeUnavailable, q.judgeUnavailableSeen = true, true
			q.notify()
		}
		return true
	}
	if ev.Event == egress.EventNetworkHold && ev.NetworkHold != nil {
		if ev.NetworkHold.Version != api.NetworkHoldV1 || ev.NetworkHold.Revision == 0 {
			return false
		}
		q.networkRevision = max(q.networkRevision, ev.NetworkHold.Revision)
		q.notify()
		return true
	}
	if ev.Event == egress.EventAudit && ev.Audit != nil {
		q.auditSeq++
		a := queuedNetAudit{entry: *ev.Audit, seq: q.auditSeq, bytes: bytes}
		if len(q.audits) >= maxQueuedNetAudits || q.auditBytes+bytes > maxQueuedAuditBytes {
			q.emergency = &a
			q.fault = true
			q.notify()
			return false
		}
		q.audits = append(q.audits, a)
		q.auditBytes += bytes
		q.notify()
		return true
	}
	ended, ok := leaseEndOf(ev)
	if !ok {
		return true
	}
	if _, expected := q.known[ended.identity]; !expected {
		return true
	}
	ended.auditSeq = q.auditSeq
	key := leaseEndKey{ended.identity, ended.expiry}
	if _, exists := q.ends[key]; !exists {
		q.order = append(q.order, key)
	}
	q.ends[key] = ended
	q.notify()
	return true
}

func (q *egressEvents) takeAudits() ([]queuedNetAudit, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	batch := q.audits
	q.audits = nil
	q.auditBytes = 0
	if q.emergency != nil {
		batch = append(batch, *q.emergency)
		q.emergency = nil
	}
	return batch, q.fault
}

func (q *egressEvents) takeEnds(audited uint64, now time.Time, closing bool) []leaseEnd {
	q.mu.Lock()
	defer q.mu.Unlock()
	var batch []leaseEnd
	kept := q.order[:0]
	for _, key := range q.order {
		ended := q.ends[key]
		future := ended.expiry && !ended.identity.until.IsZero() && now.Before(ended.identity.until)
		if ended.auditSeq <= audited && (!future || closing) {
			batch = append(batch, ended)
			delete(q.ends, key)
		} else {
			kept = append(kept, key)
		}
	}
	q.order = kept
	return batch
}

func (q *egressEvents) hasEnds() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ends) > 0
}

func (q *egressEvents) nextDeadline() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	var next time.Time
	for _, ev := range q.ends {
		if ev.expiry && !ev.identity.until.IsZero() && (next.IsZero() || ev.identity.until.Before(next)) {
			next = ev.identity.until
		}
	}
	return next
}

func (q *egressEvents) empty() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.audits) == 0 && q.emergency == nil && len(q.ends) == 0 && q.networkRevision == 0 && len(q.classifierSessions) == 0 && !q.judgeUnavailable && !q.modelBudget
}

func (q *egressEvents) takeNetworkRevision() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	revision := q.networkRevision
	q.networkRevision = 0
	return revision
}
