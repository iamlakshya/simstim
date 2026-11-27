package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
)

func TestAutoWallMetadataUsesPinnedChoiceAndKeepsDefaultMinimum(t *testing.T) {
	host := Host{Name: "box", WallFloor: "gvisor"}
	choice, minimum, err := chooseAutoPlacementWallV1(host, TierOwn, NewArgs{}, nil, PlacementNeed{})
	if err != nil || choice.Level != "gvisor" || minimum != "microvm" {
		t.Fatal("auto upgraded pinned choice or weakened its default minimum", choice, minimum, err)
	}
	choice, minimum, err = chooseAutoPlacementWallV1(host, TierOwn, NewArgs{Wall: "gvisor"}, nil, PlacementNeed{})
	if err != nil || choice.Level != "gvisor" || minimum != "gvisor" {
		t.Fatal(choice, minimum, err)
	}
	host.WallFloor = ""
	if _, _, err := chooseAutoPlacementWallV1(host, TierOwn, NewArgs{}, nil, PlacementNeed{}); err == nil {
		t.Fatal("an unpinned host acquired a placement default")
	}
}

func TestAutoConfiguredWallRaisesOnlyThroughCanonicalFloorRule(t *testing.T) {
	level := "gvisor"
	layers := []api.Layer{{Kind: api.LayerProfile, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &level}}}}
	host := Host{Name: "box", WallFloor: "microvm"}
	choice, minimum, err := chooseAutoPlacementWallV1(host, TierOwn, NewArgs{}, layers, PlacementNeed{})
	if err != nil || choice.Level != "microvm" || minimum != "gvisor" {
		t.Fatal(choice, minimum, err)
	}
	if _, _, err := chooseAutoPlacementWallV1(host, TierOwn, NewArgs{Wall: "gvisor"}, layers, PlacementNeed{}); err == nil {
		t.Fatal("explicit named wall was silently raised instead of preserving literal intent")
	}
}
