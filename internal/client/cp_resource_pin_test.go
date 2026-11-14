package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCreationCopyResourceUsesOriginalLocalCeilingAndRestriction(t *testing.T) {
	local, checked := "512M", "64M"
	args := NewArgs{Layers: []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &local}}}, {Kind: api.LayerSession}}, RepositoryPolicy: &api.RepositoryPolicy{Layer: api.Layer{Kind: api.LayerRepo, Name: "repo", Source: api.SourceRepositoryFile, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &checked}}}}}
	raw, err := encodeLocalPolicySources(args)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := api.DecodePolicySourcePin(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Resources.CPMax == nil || *pin.Resources.CPMax != 64<<20 {
		t.Fatal("checked repository data raised or erased local copy ceiling")
	}
	checked = "1G"
	raw, err = encodeLocalPolicySources(args)
	if err != nil {
		t.Fatal(err)
	}
	pin, err = api.DecodePolicySourcePin(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Resources.CPMax == nil || *pin.Resources.CPMax != 512<<20 {
		t.Fatal("checked repository data raised local copy approval")
	}
}
