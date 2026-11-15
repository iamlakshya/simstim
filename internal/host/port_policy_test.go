package host

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPortPolicyKeepsOrderDenialsExpiryAndSource(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	base := api.Rule{Door: api.DoorPort, Layer: api.LayerAll, Target: "3000", Allow: true}
	session := base
	session.Layer = api.LayerSession
	policy := &api.Policy{Order: []api.LayerRef{{Kind: api.LayerAll}, {Kind: api.LayerSession}}, Rules: []api.Rule{base, session}}
	if allowed, _, err := PortPolicyGrant(policy, 3000, now); err != nil || !allowed {
		t.Fatalf("ordered allowance = %v, %v", allowed, err)
	}
	policy.Rules[1].Allow = false
	if allowed, _, err := PortPolicyGrant(policy, 3000, now); err != nil || allowed {
		t.Fatalf("narrow deny = %v, %v", allowed, err)
	}
	policy.Rules[1].Until = now.Add(-time.Nanosecond)
	if allowed, _, err := PortPolicyGrant(policy, 3000, now); err != nil || !allowed {
		t.Fatalf("expired narrow deny = %v, %v", allowed, err)
	}
	policy.Rules = []api.Rule{session}
	policy.Rules[0].Until = now.Add(time.Second)
	if allowed, until, err := PortPolicyGrant(policy, 3000, now); err != nil || !allowed || !until.Equal(now.Add(time.Second)) {
		t.Fatalf("exact expiry = %v, %v, %v", allowed, until, err)
	}
	if allowed, _, err := PortPolicyGrant(policy, 3000, now.Add(time.Second)); err != nil || allowed {
		t.Fatalf("expired allowance = %v, %v", allowed, err)
	}
	policy.Rules[0].Until = time.Time{}
	policy.Rules[0].Source = api.SourceRepositoryFile
	policy.Order = []api.LayerRef{{Kind: api.LayerSession, Source: api.SourceRepositoryFile}}
	if allowed, _, err := PortPolicyGrant(policy, 3000, now); err == nil || allowed {
		t.Fatalf("repository allowance = %v, %v", allowed, err)
	}
}
