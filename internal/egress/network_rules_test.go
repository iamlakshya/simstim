package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func methodSet(values ...string) *[]string { return &values }
func TestStructuredNetworkRulesPreserveMethodPresenceAndExactRemoval(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Allow: true, Target: "api.example.com", Methods: methodSet("POST", "GET", "GET")}, {Allow: true, Target: "api.example.com"}, {Target: "other.example.com", Methods: methodSet()}}}}}, {Kind: api.LayerSession, LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Allow: true, Target: "api.example.com", Methods: methodSet("GET", "POST"), Remove: true}}}}}}
	policy, clean, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	var unrestricted, presentEmpty bool
	for _, rule := range policy.Rules {
		if rule.Target == "api.example.com" {
			if rule.Methods != nil {
				t.Fatal("removed constrained tuple survived")
			}
			unrestricted = true
		}
		if rule.Target == "other.example.com" {
			presentEmpty = rule.Methods != nil && len(*rule.Methods) == 0
		}
	}
	if !unrestricted || !presentEmpty {
		t.Fatal("nil/empty method semantics changed")
	}
	if len(clean[0].Network.Rules) != 0 || len(clean[1].Network.Rules) != 1 || !clean[1].Network.Rules[0].Remove {
		t.Fatal("raw additions resurrect or removal directive vanished")
	}
	again, _, err := MergeLayers(clean)
	if err != nil || len(again.Rules) != len(policy.Rules) {
		t.Fatalf("restart replay changed removal: %v", err)
	}
}

func TestStructuredLockKeepsScopedDenyAndRejectsOnlyMethodOverlap(t *testing.T) {
	lock := &api.Lock{LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Target: "api.example.com", Methods: methodSet("GET")}}}}}
	for _, test := range []struct {
		methods *[]string
		locked  bool
	}{{methodSet("POST"), false}, {methodSet("GET"), true}, {nil, true}, {methodSet(), false}} {
		policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, Lock: lock}, {Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "api.example.com", Methods: test.methods}}}})
		if test.locked {
			if err == nil {
				t.Fatal("overlapping method grant bypassed lock")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(policy.Locks) != 1 || len(policy.Locks[0].Deny) != 0 || len(policy.Locks[0].Network.Rules) != 1 {
			t.Fatal("scoped lock broadened into unrestricted deny")
		}
	}
}
