package host

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestPublicOnlyActualSessionAddRegrantAndColdReplay(t *testing.T) {
	args := api.PolicyAddArgs{Allow: true, Target: "api.example.com", Once: true, PublicOnly: true}
	first, layers, rule, err := ApplyPolicyAdd(nil, args)
	if err != nil || !rule.PublicOnly || !first.Rules[0].PublicOnly || !layers[0].Rules[0].PublicOnly {
		t.Fatal("actual session writer dropped the restriction", err)
	}
	_, next, second, err := ApplyPolicyAdd(layers, args)
	if err != nil || second.GrantID == rule.GrantID || second.ID != rule.ID || len(next[0].Rules) != 2 {
		t.Fatal("restricted once regrant lost independent lease instances", err)
	}
	raw, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	var cold []api.Layer
	if err := json.Unmarshal(raw, &cold); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := egress.MergeLayers(cold)
	if err != nil || len(resumed.Rules) != 2 {
		t.Fatal("cold resume changed restricted leases", err)
	}
	for _, row := range resumed.Rules {
		if !row.PublicOnly || !row.Once {
			t.Fatal("cold resume widened a restricted lease")
		}
	}
	args.PublicOnly = false
	args.Once = false
	policy, _, ordinary, err := ApplyPolicyAdd(next, args)
	if err != nil || ordinary.ID == rule.ID || len(policy.Rules) != 3 {
		t.Fatal("independent explicit ordinary grant erased restricted tuple", err)
	}
	if !layers[0].Rules[0].PublicOnly {
		t.Fatal("addition mutated prior layers")
	}
}
