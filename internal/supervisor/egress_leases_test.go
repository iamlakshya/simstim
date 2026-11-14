package supervisor

import (
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestLeaseEndMatchesOnlyTheExactGrant(t *testing.T) {
	now := time.Date(2500, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	rule := api.Rule{ID: "rule", GrantID: "current", Once: true, Until: now}
	once := leaseEnd{identity: ruleLeaseIdentity(rule)}
	if !once.matches(rule, now) {
		t.Fatal("the exact once lease did not match")
	}
	for name, changed := range map[string]api.Rule{
		"new ID":            {ID: "other", GrantID: rule.GrantID, Once: true, Until: now},
		"new grant":         {ID: rule.ID, GrantID: "replacement", Once: true, Until: now},
		"changed deadline":  {ID: rule.ID, GrantID: rule.GrantID, Once: true, Until: now.Add(time.Nanosecond)},
		"permanent upgrade": {ID: rule.ID, GrantID: rule.GrantID, Once: false, Until: now},
		"legacy empty":      {ID: rule.ID, GrantID: "", Once: true, Until: now},
	} {
		if once.matches(changed, now) {
			t.Errorf("old once event removed %s", name)
		}
	}
	legacy := rule
	legacy.GrantID = ""
	legacyEnd := leaseEnd{identity: ruleLeaseIdentity(legacy)}
	if !legacyEnd.matches(legacy, now) || legacyEnd.matches(rule, now) {
		t.Fatal("an empty legacy identity was treated as a wildcard")
	}
}

func TestExpiryMatchesOnlyAnExpiredNonzeroDeadline(t *testing.T) {
	now := time.Date(2500, time.January, 2, 3, 4, 5, 987654321, time.UTC)
	for _, once := range []bool{false, true} {
		for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			rule := api.Rule{ID: "rule", GrantID: "current", Once: once, Until: now.Add(offset)}
			ended := leaseEnd{identity: ruleLeaseIdentity(rule), expiry: true}
			if got, want := ended.matches(rule, now), offset <= 0; got != want {
				t.Errorf("once %v, offset %v: match %v, want %v", once, offset, got, want)
			}
		}
	}
	zero := api.Rule{ID: "rule", GrantID: "current", Once: true}
	if (leaseEnd{identity: ruleLeaseIdentity(zero), expiry: true}).matches(zero, now) {
		t.Fatal("expiry removed a zero-deadline lease")
	}
}

func TestLeaseTimeIdentityIsLosslessAndCanonical(t *testing.T) {
	first := time.Date(2000, time.January, 1, 0, 0, 0, 123456789, time.UTC)
	second := first
	for range 4 {
		second = second.Add(time.Duration(1 << 62))
	}
	if first.UnixNano() != second.UnixNano() || first.Equal(second) {
		t.Fatal("test did not construct distinct dates with the same wrapped UnixNano")
	}
	a := ruleLeaseIdentity(api.Rule{ID: "rule", Until: first})
	b := ruleLeaseIdentity(api.Rule{ID: "rule", Until: second})
	if a == b {
		t.Fatal("lease identity reduced a full date to wrapped nanoseconds")
	}
	shifted := first.In(time.FixedZone("offset", 3600))
	if a != ruleLeaseIdentity(api.Rule{ID: "rule", Until: shifted}) {
		t.Fatal("equivalent locations produced different lease identities")
	}
	if canonicalLeaseTime(time.Time{}) != (time.Time{}) || canonicalLeaseTime(time.Time{}.UTC()) != (time.Time{}) {
		t.Fatal("zero deadline did not retain its canonical zero representation")
	}
}

func TestRemovingLeaseEventsIsTransactional(t *testing.T) {
	now := time.Date(2500, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	old := api.Rule{ID: "old", GrantID: "instance", Once: true, Target: "example.com"}
	keep := api.Rule{ID: "keep", GrantID: "fresh", Once: true, Target: "other.example"}
	layers := []api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{old, keep}, Lock: &api.Lock{Deny: []string{"blocked.example"}}}}
	before := []api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{old, keep}, Lock: &api.Lock{Deny: []string{"blocked.example"}}}}
	events := []leaseEnd{{identity: ruleLeaseIdentity(old)}, {identity: ruleLeaseIdentity(old)}}
	next, removed := removeEndedLeases(layers, events, now)
	if len(removed) != 1 || removed[0].GrantID != old.GrantID || len(next[0].Rules) != 1 || next[0].Rules[0].GrantID != keep.GrantID {
		t.Fatalf("wrong leases removed or survivors changed: %+v, %+v", removed, next)
	}
	next[0].Rules[0].GrantID = "mutated"
	next[0].Lock.Deny[0] = "mutated.example"
	if !reflect.DeepEqual(layers, before) {
		t.Fatal("lease removal retained mutable aliases to the original manifest")
	}
}

func TestLeaseEventTypesStayDistinct(t *testing.T) {
	until := time.Date(2500, time.January, 2, 3, 4, 5, 7, time.UTC)
	identity := egress.OnceEndEvent{ID: "rule", GrantID: "instance", Until: until}
	once, ok := leaseEndOf(egress.CtlEvent{Event: egress.EventOnceEnd, OnceEnd: &identity})
	if !ok || once.expiry {
		t.Fatal("once end was not decoded as a once event")
	}
	expiryIdentity := egress.RuleExpiryEvent(identity)
	expiry, ok := leaseEndOf(egress.CtlEvent{Event: egress.EventRuleExpiry, RuleExpiry: &expiryIdentity})
	if !ok || !expiry.expiry || expiry.identity != once.identity {
		t.Fatal("expiry event did not retain its exact lease identity")
	}
	if _, ok := leaseEndOf(egress.CtlEvent{Event: egress.EventOnceEnd}); ok {
		t.Fatal("missing event body was accepted")
	}
}
