package api

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestPublicOnlyWireCloneAndLegacyIdentity(t *testing.T) {
	legacy := Rule{Layer: LayerSession, Door: DoorNet, Target: "api.example.com", Allow: true}
	if RuleID(legacy) != "g6jnnd7" {
		t.Fatal("absent restriction changed legacy rule identity")
	}
	data, err := json.Marshal(legacy)
	if err != nil || bytes.Contains(data, []byte("public_only")) {
		t.Fatal("legacy JSON grew a restriction field")
	}
	restricted := legacy
	restricted.PublicOnly = true
	if RuleID(restricted) == RuleID(legacy) {
		t.Fatal("restricted identity aliased the ordinary allowance")
	}
	layer := Layer{Kind: LayerSession, Rules: []Rule{restricted}, LayerValues: LayerValues{Network: NetworkValues{Rules: []NetworkRule{{Target: "api.example.com", Allow: true, PublicOnly: true}}}}}
	copy := CloneLayers([]Layer{layer})
	policy := ClonePolicy(Policy{Rules: []Rule{restricted}, Sources: copy})
	if !policy.Rules[0].PublicOnly || !policy.Sources[0].Rules[0].PublicOnly || !policy.Sources[0].Network.Rules[0].PublicOnly {
		t.Fatal("deep clone dropped a restriction")
	}
	data, err = json.Marshal(copy)
	var restored []Layer
	if err != nil || json.Unmarshal(data, &restored) != nil || !reflect.DeepEqual(copy, restored) {
		t.Fatal("cold layer JSON lost the restriction")
	}
	var encoded bytes.Buffer
	raw := NetworkRule{Target: "api.example.com", Allow: true, PublicOnly: true}
	if err := toml.NewEncoder(&encoded).Encode(raw); err != nil {
		t.Fatal(err)
	}
	var decoded NetworkRule
	if _, err := toml.Decode(encoded.String(), &decoded); err != nil || !decoded.PublicOnly || decoded.Target != raw.Target {
		t.Fatal("file tuple TOML dropped the restriction")
	}
	args := PolicyAddArgs{Allow: true, Target: "api.example.com", PublicOnly: true}
	data, err = json.Marshal(args)
	var answer PolicyAddArgs
	if err != nil || json.Unmarshal(data, &answer) != nil || !answer.PublicOnly {
		t.Fatal("session addition wire dropped the restriction")
	}
}

func TestPublicOnlyFeatureDetectionCoversSavedCarriers(t *testing.T) {
	if PolicyUsesPublicOnly(Policy{}) || LayersUsePublicOnly([]Layer{{Rules: []Rule{{Allow: true, Target: "api.example.com"}}}}) {
		t.Fatal("ordinary policy acquired a new feature requirement")
	}
	row := Rule{Allow: true, PublicOnly: true}
	raw := NetworkRule{Allow: true, PublicOnly: true}
	for _, policy := range []Policy{
		{Rules: []Rule{row}},
		{LayerValues: LayerValues{Network: NetworkValues{Rules: []NetworkRule{raw}}}},
		{Sources: []Layer{{Rules: []Rule{row}}}},
		{Locks: []Lock{{LayerValues: LayerValues{Network: NetworkValues{Rules: []NetworkRule{raw}}}}}},
	} {
		if !PolicyUsesPublicOnly(policy) {
			t.Fatal("a saved carrier hid its restriction requirement")
		}
	}
	if !LayersUsePublicOnly([]Layer{{Lock: &Lock{LayerValues: LayerValues{Network: NetworkValues{Rules: []NetworkRule{raw}}}}}}) {
		t.Fatal("a lock carrier hid the restriction requirement")
	}
}
