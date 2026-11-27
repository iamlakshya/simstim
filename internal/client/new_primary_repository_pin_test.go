package client

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestExactPrimaryRepositoryPinUsesExistingFormatAndRefreshesNewSection(t *testing.T) {
	for _, organisation := range []bool{false, true} {
		settings := DefaultSettings()
		if organisation {
			settings.Repos = map[string]RepoSettings{"forge.example/team/*": {LayerValues: api.LayerValues{Network: api.NetworkValues{Allow: []string{"prior.example"}}}}}
		}
		layers, err := ResolvePolicyLayers(settings, Host{Name: "box"}, "https://forge.example/team/app.git", "", api.LayerValues{})
		if err != nil {
			t.Fatal(err)
		}
		pinned, err := withExactPrimaryRepositorySourceV1(layers, "forge.example/team/app")
		if err != nil {
			t.Fatal(err)
		}
		pin, err := api.PinPolicySources(pinned)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(pin)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := api.DecodePolicySourcePin(raw)
		if err != nil || !slices.ContainsFunc(decoded.Layers, func(ref api.LayerRef) bool { return ref.Kind == api.LayerRepo && ref.Name == "forge.example/team/app" }) {
			t.Fatal("the original exact identity did not survive the existing pin codec", err)
		}
		if settings.Repos == nil {
			settings.Repos = map[string]RepoSettings{}
		}
		settings.Repos["forge.example/team/app"] = RepoSettings{LayerValues: api.LayerValues{Network: api.NetworkValues{Allow: []string{"new.example"}}}}
		refreshed, err := refreshPolicyLayers(settings, Host{Name: "box"}, pinned)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, layer := range refreshed {
			if layer.Kind == api.LayerRepo && layer.Name == "forge.example/team/app" {
				found = slices.Contains(layer.Network.Allow, "new.example")
			}
		}
		if !found {
			t.Fatal("a new exact section was omitted from the original source refresh")
		}
	}
}

func TestExactPrimaryRepositoryLayerPreservesOrderingAndExistingLiteral(t *testing.T) {
	layers := []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: "forge.example/team/*"}, {Kind: api.LayerProfile, Name: "review"}, {Kind: api.LayerSession}}
	got, err := withExactPrimaryRepositorySourceV1(layers, "forge.example/team/app")
	if err != nil || len(got) != 6 || got[3].Name != "forge.example/team/app" || got[4].Kind != api.LayerProfile {
		t.Fatal("exact primary did not follow matching repositories before profile", err)
	}
	again, err := withExactPrimaryRepositorySourceV1(got, "forge.example/team/app")
	if err != nil || len(again) != len(got) || len(layers) != 5 {
		t.Fatal("original exact source was duplicated or original layers changed", err)
	}
	if _, err := withExactPrimaryRepositorySourceV1(layers, "forge.example/team/*"); err == nil {
		t.Fatal("an organisation pattern became exact primary provenance")
	}
}
