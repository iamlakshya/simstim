package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestClonePolicyOwnsAllCurrentMutableMetadata(t *testing.T) {
	empty := []string{}
	methods := []string{"GET"}
	memory := "4G"
	until := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	original := Policy{
		LayerValues: LayerValues{Sandbox: SandboxValues{Memory: &memory, Env: map[string]string{"MODE": "safe"}, CopyIn: []string{"relative:work/file"}},
			Network: NetworkValues{Allow: []string{"first.example"}, Rules: []NetworkRule{{Target: "second.example", Methods: &empty}}}},
		Sources: []Layer{{Kind: LayerHost, Name: "saved", LayerValues: LayerValues{Sandbox: SandboxValues{Env: map[string]string{"SOURCE": "original"}}},
			Rules: []Rule{{Target: "third.example", Methods: &empty}}, Lock: &Lock{LayerValues: LayerValues{Sandbox: SandboxValues{Keep: []string{"cache"}}}}}},
		Order:   []LayerRef{{Kind: LayerHost, Name: "saved"}},
		Origins: []ValueOrigin{{Key: "sandbox.memory", Layer: LayerHost, Name: "saved"}},
		Rules:   []Rule{{Target: "fourth.example", Methods: &methods, Until: until}, {Target: "fifth.example", Methods: nil}, {Target: "sixth.example", Methods: &empty}},
		Locks:   []Lock{{LayerValues: LayerValues{Network: NetworkValues{Deny: []string{"blocked.example"}}}}},
	}
	copy := ClonePolicy(original)
	*copy.Sandbox.Memory = "8G"
	copy.Sandbox.Env["MODE"] = "changed"
	copy.Sandbox.CopyIn[0] = "changed"
	copy.Network.Allow[0] = "changed"
	*copy.Network.Rules[0].Methods = append(*copy.Network.Rules[0].Methods, "POST")
	copy.Sources[0].Sandbox.Env["SOURCE"] = "changed"
	*copy.Sources[0].Rules[0].Methods = append(*copy.Sources[0].Rules[0].Methods, "POST")
	copy.Sources[0].Lock.Sandbox.Keep[0] = "changed"
	copy.Order[0].Name = "changed"
	copy.Origins[0].Name = "changed"
	(*copy.Rules[0].Methods)[0] = "POST"
	copy.Locks[0].Network.Deny[0] = "changed"
	if *original.Sandbox.Memory != "4G" || original.Sandbox.Env["MODE"] != "safe" || original.Sandbox.CopyIn[0] != "relative:work/file" || original.Network.Allow[0] != "first.example" {
		t.Fatal("policy value data was shared")
	}
	if len(*original.Network.Rules[0].Methods) != 0 || len(*original.Sources[0].Rules[0].Methods) != 0 || original.Sources[0].Sandbox.Env["SOURCE"] != "original" || original.Sources[0].Lock.Sandbox.Keep[0] != "cache" {
		t.Fatal("source/method data was shared")
	}
	if original.Order[0].Name != "saved" || original.Origins[0].Name != "saved" || (*original.Rules[0].Methods)[0] != "GET" || original.Locks[0].Network.Deny[0] != "blocked.example" {
		t.Fatal("policy rule/order/origin/lock data was shared")
	}
	if copy.Rules[1].Methods != nil || copy.Rules[2].Methods == nil || *copy.Rules[2].Methods == nil || len(*copy.Rules[2].Methods) != 0 || !copy.Rules[0].Until.Equal(until) {
		t.Fatal("method presence or time instant changed")
	}
	original.Sources[0].Sandbox.Env["SOURCE"] = "new original"
	if copy.Sources[0].Sandbox.Env["SOURCE"] != "changed" {
		t.Fatal("original mutation reached the clone")
	}
}

func TestPolicyClonePreservesEmptyAndOpaqueSourceCarriers(t *testing.T) {
	original := Policy{Sources: []Layer{}, Order: []LayerRef{}, Origins: []ValueOrigin{}, Rules: []Rule{}, Locks: []Lock{}, LayerValues: LayerValues{Sandbox: SandboxValues{Env: map[string]string{}}}}
	copy := ClonePolicy(original)
	if copy.Sources == nil || copy.Order == nil || copy.Origins == nil || copy.Rules == nil || copy.Locks == nil || copy.Sandbox.Env == nil {
		t.Fatal("known empty became absent")
	}
	zero := ClonePolicy(Policy{})
	if zero.Sources != nil || zero.Order != nil || zero.Origins != nil || zero.Rules != nil || zero.Locks != nil || zero.Sandbox.Env != nil {
		t.Fatal("absence became known empty")
	}
	// Opaque source bytes belong to the outer source-pin carrier, not Policy.
	// Exercise the SAME cloner on that concrete raw-slice shape without a
	// decoding/roundtrip or treating its future contents as authorisation.
	type carrier struct {
		Policy Policy
		Source json.RawMessage
	}
	before := carrier{Policy: original, Source: json.RawMessage(`{"future":2,"unknown":[1]}`)}
	after := cloneLayerData(reflect.ValueOf(before)).Interface().(carrier)
	after.Source[0] = '['
	if before.Source[0] != '{' {
		t.Fatal("opaque source bytes were shared")
	}
}
