package api

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestPolicyResourcePinOwnsCreationProjection(t *testing.T) {
	value := "1G"
	layers := []Layer{{Kind: LayerProfile, Name: "work", LayerValues: LayerValues{Sandbox: SandboxValues{PullMax: &value}}}, {Kind: LayerSession}}
	pin, err := PinPolicySourcesWithResources(layers, PolicyResourcePin{Version: 1, PullMax: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	value = "2G"
	raw, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePolicySourcePin(raw)
	if err != nil || decoded.Resources == nil || decoded.Resources.PullMax != 1<<30 {
		t.Fatalf("creation projection changed: %v, %v", decoded, err)
	}
	if decoded.Session.Sandbox.PullMax != nil {
		t.Fatal("a historical profile value was fabricated as a session override")
	}
	legacy, err := PinPolicySources(layers)
	if err != nil || legacy.Resources != nil {
		t.Fatal("legacy builder invented a resource projection")
	}
}

func TestPolicyResourcePinStrictBoundary(t *testing.T) {
	for _, resource := range []string{
		`{"version":1,"pull_max":0}`, `{"version":1,"pull_max":-1}`,
		`{"version":2,"pull_max":1024}`, `{"pull_max":1024}`,
		`{"version":1}`, `{"Version":1,"pull_max":1024}`,
		`{"version":1,"version":1,"pull_max":1024}`,
		`{"version":1,"pull_max":1024,"Pull_Max":2048}`,
		`{"version":1,"pull_max":1024,"future":true}`,
		`{"version":1,"pull_max":4611686018427387904}`,
		`{"version":1,"pull_max":9223372036854775808}`, `null`,
	} {
		raw := `{"layers":[{"kind":"session"}],"session":{},"resources":` + resource + `}`
		if _, err := DecodePolicySourcePin(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted resource projection %s", resource)
		}
	}
	for _, n := range []int64{1, 1<<62 - 1} {
		if _, err := PinPolicySourcesWithResources([]Layer{{Kind: LayerSession}}, PolicyResourcePin{Version: 1, PullMax: n}); err != nil {
			t.Fatalf("rejected existing size range %d: %v", n, err)
		}
	}
	if _, err := PinPolicySourcesWithResources([]Layer{{Kind: LayerSession}}, PolicyResourcePin{Version: 1, PullMax: math.MaxInt64}); err == nil {
		t.Fatal("integer bypassed canonical size range")
	}
	old := json.RawMessage(`{"layers":[{"kind":"session"}],"session":{"sandbox":{"pull_max":"1G"}}}`)
	pin, err := DecodePolicySourcePin(old)
	if err != nil || pin.Resources != nil || pin.Session.Sandbox.PullMax == nil || !strings.Contains(string(old), "1G") {
		t.Fatal("old pin no longer decodes without a projection", err)
	}
}
