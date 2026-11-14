package supervisor

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func registerEventPolicy(t *testing.T, q *egressEvents, rules ...api.Rule) {
	t.Helper()
	raw, err := json.Marshal(egress.PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetLocked, Rules: rules}})
	if err != nil {
		t.Fatal(err)
	}
	q.preparePolicy(egress.CtlPolicyUpdate, raw)(true)
}

func onceEvent(rule api.Rule) egress.CtlEvent {
	return egress.CtlEvent{Event: egress.EventOnceEnd, OnceEnd: &egress.OnceEndEvent{ID: rule.ID, GrantID: rule.GrantID, Until: rule.Until}}
}

func expiryEvent(rule api.Rule) egress.CtlEvent {
	return egress.CtlEvent{Event: egress.EventRuleExpiry, RuleExpiry: &egress.RuleExpiryEvent{ID: rule.ID, GrantID: rule.GrantID, Until: rule.Until}}
}

func TestEndQueueCoalescesOnlyExactRegisteredLeases(t *testing.T) {
	q := newEgressEvents()
	rule := api.Rule{ID: "rule", GrantID: "current", Once: true}
	registerEventPolicy(t, q, rule)
	for range 100 {
		if !q.enqueue(onceEvent(rule), 128) {
			t.Fatal("duplicate event failed queue")
		}
	}
	legacy := rule
	legacy.GrantID = ""
	q.enqueue(onceEvent(legacy), 128)
	changed := rule
	changed.Until = time.Date(2500, 1, 1, 0, 0, 0, 1, time.UTC)
	q.enqueue(onceEvent(changed), 128)
	if len(q.ends) != 1 || len(q.order) != 1 {
		t.Fatalf("duplicates or foreign events retained: %d, %d", len(q.ends), len(q.order))
	}
	batch := q.takeEnds(0, time.Now(), false)
	if len(batch) != 1 || batch[0].identity != ruleLeaseIdentity(rule) {
		t.Fatalf("wrong queued lease: %+v", batch)
	}
}

func TestPendingPolicyIdentityAcceptsAnEventBeforeItsReply(t *testing.T) {
	q := newEgressEvents()
	old := api.Rule{ID: "rule", GrantID: "old", Once: true}
	fresh := old
	fresh.GrantID = "fresh"
	registerEventPolicy(t, q, old)
	raw, _ := json.Marshal(egress.PolicyUpdateArgs{Policy: api.Policy{Rules: []api.Rule{fresh}}})
	finish := q.preparePolicy(egress.CtlPolicyUpdate, raw)
	q.enqueue(onceEvent(fresh), 128)
	finish(true)
	batch := q.takeEnds(0, time.Now(), false)
	if len(batch) != 1 || batch[0].identity.grantID != "fresh" {
		t.Fatalf("new event was lost before manifest publication: %+v", batch)
	}
}

func TestRetiredLeaseChurnCannotGrowTheEndQueue(t *testing.T) {
	q := newEgressEvents()
	until := time.Date(2500, 1, 1, 0, 0, 0, 1, time.UTC)
	for i := range 1000 {
		rule := api.Rule{ID: "rule", GrantID: fmt.Sprint(i), Until: until}
		registerEventPolicy(t, q, rule)
		q.enqueue(expiryEvent(rule), 128)
		if len(q.ends) > 1 || len(q.order) > 1 || len(q.known) > 1 {
			t.Fatal("retired generations accumulated")
		}
	}
	for i := range 1000 {
		q.enqueue(onceEvent(api.Rule{ID: fmt.Sprint(i), GrantID: "foreign"}), 128)
	}
	if len(q.ends) != 1 {
		t.Fatal("foreign event churn accumulated")
	}
}

func TestAuditBarrierAndOverflowRetainEveryReceivedRecord(t *testing.T) {
	q := newEgressEvents()
	rule := api.Rule{ID: "rule", GrantID: "current", Once: true}
	registerEventPolicy(t, q, rule)
	audit := egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com"}}
	q.enqueue(audit, 128)
	q.enqueue(onceEvent(rule), 128)
	if got := q.takeEnds(0, time.Now(), false); len(got) != 0 {
		t.Fatal("end crossed its audit barrier")
	}
	for i := 1; i < maxQueuedNetAudits; i++ {
		if !q.enqueue(audit, 128) {
			t.Fatalf("unexpected overflow at %d", i)
		}
	}
	if q.enqueue(audit, 128) {
		t.Fatal("audit count overflow was not fail closed")
	}
	batch, fault := q.takeAudits()
	if !fault || len(batch) != maxQueuedNetAudits+1 {
		t.Fatalf("received overflow frame lost: %v, %d", fault, len(batch))
	}
	for i, a := range batch {
		if a.seq != uint64(i+1) {
			t.Fatal("audit order changed")
		}
	}
	if got := q.takeEnds(batch[len(batch)-1].seq, time.Now(), true); len(got) != 1 {
		t.Fatal("legitimate end was dropped on overflow")
	}
	q = newEgressEvents()
	for range 16 {
		if !q.enqueue(audit, maxEvent) {
			t.Fatal("byte limit reached early")
		}
	}
	if q.enqueue(audit, maxEvent) || q.emergency == nil || q.auditBytes > maxQueuedAuditBytes {
		t.Fatal("audit byte bound or emergency frame failed")
	}
}

func TestEarlyExpiryWaitsForTrustedDeadline(t *testing.T) {
	q := newEgressEvents()
	now := time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC)
	rule := api.Rule{ID: "rule", GrantID: "current", Until: now.Add(time.Nanosecond)}
	registerEventPolicy(t, q, rule)
	q.enqueue(expiryEvent(rule), 128)
	if got := q.takeEnds(0, now, false); len(got) != 0 {
		t.Fatal("future expiry was processed early")
	}
	if !q.nextDeadline().Equal(rule.Until) {
		t.Fatal("future expiry was not retained for its trusted deadline")
	}
	if got := q.takeEnds(0, rule.Until, false); len(got) != 1 {
		t.Fatal("due expiry event was lost")
	}
}
