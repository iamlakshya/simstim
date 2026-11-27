package client

import (
	"bytes"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestPublicOnlyFileRuleStaysStructuredAndIdentityRoundTrips(t *testing.T) {
	section := map[string]any{}
	rule := api.Rule{Layer: api.LayerProfile, Name: "work", Door: api.DoorNet, Allow: true, Target: "8.8.8.8:443", PublicOnly: true}
	rule.ID = api.RuleID(rule)
	changed, _, err := editFileRules(section, rule, "")
	if err != nil || !changed {
		t.Fatal("restricted rule not written", err)
	}
	var text bytes.Buffer
	if err := toml.NewEncoder(&text).Encode(section); err != nil {
		t.Fatal(err)
	}
	var values api.LayerValues
	if _, err := decodeConfig(text.String(), &values); err != nil {
		t.Fatal(err)
	}
	if len(values.Network.Allow) != 0 || len(values.Network.Rules) != 1 || !values.Network.Rules[0].PublicOnly {
		t.Fatal("public_only downgraded to an unrestricted string")
	}
	policy, _, err := egress.MergeLayers([]api.Layer{{Kind: api.LayerProfile, Name: "work", LayerValues: values}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, got := range policy.Rules {
		if got.Target == rule.Target {
			found = true
			if !got.PublicOnly || got.ID != rule.ID {
				t.Fatal("cold merge changed restriction or identity")
			}
		}
	}
	if !found {
		t.Fatal("cold merge lost original restricted rule")
	}
	changed, removed, err := editFileRules(section, api.Rule{Layer: rule.Layer, Name: rule.Name}, rule.ID)
	if err != nil || !changed || removed != 1 {
		t.Fatal("exact restricted ID could not be removed", removed, err)
	}
}

func TestPublicOnlyOutgoingAndColdLayersRefuseOlderHelpers(t *testing.T) {
	for _, layer := range []api.Layer{
		{Rules: []api.Rule{{Allow: true, PublicOnly: true, Target: "example.test:443"}}},
		{LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Allow: true, PublicOnly: true, Target: "example.test:443"}}}}},
		{Lock: &api.Lock{LayerValues: api.LayerValues{Network: api.NetworkValues{Rules: []api.NetworkRule{{Allow: true, PublicOnly: true, Target: "example.test:443"}}}}}},
	} {
		old := &Conn{Host: Host{Name: "box"}, Features: []string{api.FeatureHTTPMethods}}
		if err := old.CheckHTTPMethodFeature([]api.Layer{layer}); err == nil {
			t.Fatal("older helper could ignore a stored restriction")
		}
		current := &Conn{Host: old.Host, Features: []string{api.FeatureNetworkPublicOnly}}
		if err := current.CheckHTTPMethodFeature([]api.Layer{layer}); err != nil {
			t.Fatal(err)
		}
	}
	old := &Conn{Host: Host{Name: "box"}}
	if _, err := old.canonicalPolicyMethods(api.PolicyAddArgs{PublicOnly: true}); err == nil {
		t.Fatal("old helper received a narrowed live mutation")
	}
}

func TestPublicOnlyChangesOnlyRestrictionNotNumericExceptionPermission(t *testing.T) {
	rule := api.Rule{Allow: true, Door: api.DoorNet, Target: "8.8.8.8:443"}
	ordinary, err := policyOutboundForRule(rule)
	if err != nil || ordinary.Kind != OutboundPrivateNet {
		t.Fatal("legacy numeric exception semantics changed", err)
	}
	rule.PublicOnly = true
	restricted, err := policyOutboundForRule(rule)
	if err != nil || restricted.Kind != OutboundNet || restricted.Name != rule.Target {
		t.Fatal("narrowed numeric rule invented a target or exception", err)
	}
	for _, invalid := range []api.Rule{{PublicOnly: true, Target: rule.Target}, {Allow: true, Door: api.DoorPush, PublicOnly: true, Target: rule.Target}} {
		if _, err := policyOutboundForRule(invalid); err == nil {
			t.Fatal("restriction applied outside allow net")
		}
	}
}
