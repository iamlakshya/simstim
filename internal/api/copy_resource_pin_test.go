package api

import (
	"encoding/json"
	"testing"
)

func TestCopyResourcePinKnownAbsentAndOwnedPointer(t *testing.T) {
	layers := []Layer{{Kind: LayerAll}, {Kind: LayerSession}}
	limit := int64(256 << 20)
	pin, err := PinPolicySourcesWithResources(layers, PolicyResourcePin{Version: 1, PullMax: 256 << 20, CPMax: &limit})
	if err != nil {
		t.Fatal(err)
	}
	limit = 1
	if pin.Resources.CPMax == nil || *pin.Resources.CPMax != 256<<20 {
		t.Fatal("resource pin aliases caller's mutable copy ceiling")
	}
	for _, size := range []int64{0, -1, 1 << 62} {
		if _, err := PinPolicySourcesWithResources(layers, PolicyResourcePin{Version: 1, PullMax: 256 << 20, CPMax: &size}); err == nil {
			t.Fatalf("invalid copy ceiling %d admitted", size)
		}
	}
	legacy, err := PinPolicySourcesWithResources(layers, PolicyResourcePin{Version: 1, PullMax: 256 << 20})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePolicySourcePin(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Resources.CPMax != nil {
		t.Fatal("legacy absence invented current copy approval")
	}
}
