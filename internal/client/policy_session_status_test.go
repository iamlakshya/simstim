package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLocalPolicyComparisonUsesOriginalPinAndMatchesActualSourceHash(t *testing.T) {
	h := Host{Name: "box"}
	settings := Settings{}
	layers, err := ResolvePolicyLayers(settings, h, "", "", api.LayerValues{})
	if err != nil {
		t.Fatal(err)
	}
	pin, err := api.PinPolicySources(layers)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := api.HashPolicyLaptopLayersV1(layers)
	got, err := localPolicyPinHash(settings, h, pin)
	if err != nil || got != want {
		t.Fatal("unchanged local files acquired a different source hash", got, want, err)
	}
	settings.Network.Deny = []string{"changed.example"}
	changed, err := localPolicyPinHash(settings, h, pin)
	if err != nil || changed == want {
		t.Fatal("hand edit did not change passive comparison", err)
	}
	if len(pin.Layers) != 3 || len(pin.Session.Network.Deny) != 0 {
		t.Fatal("passive comparison changed the original local pin")
	}
}

func TestPolicyDisplayMarkerChangesNoSessionIdentity(t *testing.T) {
	s := Session{ID: "root2345", Name: "work", PolicyChanged: true}
	if PolicyDisplayName(s) != "work~" || s.Name != "work" || s.ID != "root2345" {
		t.Fatal("passive marker changed the request row")
	}
}
