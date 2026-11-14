package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLocalPullCeilingKeepsResolvedCreationPrecedence(t *testing.T) {
	for _, test := range []struct {
		name   string
		layers []api.Layer
		file   *api.Layer
		want   int64
		refuse bool
	}{
		{name: "profile", layers: []api.Layer{pullLayer(api.LayerProfile, "work", "1G"), {Kind: api.LayerSession}}, want: 1 << 30},
		{name: "session", layers: []api.Layer{pullLayer(api.LayerAll, "", "2G"), pullLayer(api.LayerHost, "box", "3G"), pullLayer(api.LayerRepo, "example.org/team/project", "4G"), pullLayer(api.LayerProfile, "work", "1G"), pullLayer(api.LayerSession, "", "768M")}, want: 768 << 20},
		{name: "repository resource narrowing is forbidden", layers: []api.Layer{pullLayer(api.LayerAll, "", "1G"), {Kind: api.LayerSession}}, file: pullLayerPtr(api.LayerRepo, "example.org/team/project", "256M"), refuse: true},
		{name: "repository resource widening is forbidden", layers: []api.Layer{pullLayer(api.LayerAll, "", "256M"), {Kind: api.LayerSession}}, file: pullLayerPtr(api.LayerRepo, "example.org/team/project", "8G"), refuse: true},
		{name: "laptop repository resource", layers: []api.Layer{pullLayer(api.LayerAll, "", "256M"), pullLayer(api.LayerRepo, "example.org/team/project", "1G"), {Kind: api.LayerSession}}, want: 1 << 30},
		{name: "default", layers: []api.Layer{{Kind: api.LayerSession}}, want: api.PullMaxDefault},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := NewArgs{Layers: api.CloneLayers(test.layers)}
			if test.file != nil {
				test.file.Source = api.SourceRepositoryFile
				a.RepositoryPolicy = &api.RepositoryPolicy{Layer: *test.file}
			}
			raw, err := encodeLocalPolicySources(a)
			if test.refuse {
				if err == nil || raw != nil {
					t.Fatal("repository file gained a resource permission forbidden by P29")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// Later configuration/layer mutation cannot reconstruct the past.
			for i := range a.Layers {
				if a.Layers[i].Sandbox.PullMax != nil {
					*a.Layers[i].Sandbox.PullMax = "8G"
				}
			}
			pin, err := api.DecodePolicySourcePin(raw)
			if err != nil {
				t.Fatal(err)
			}
			limit, err := pullMaxFromPin(pin)
			if err != nil || limit != test.want {
				t.Fatalf("local ceiling = %d, %v; want %d", limit, err, test.want)
			}
			for _, ref := range pin.Layers {
				if ref.Source == api.SourceRepositoryFile {
					t.Fatal("repository response was adopted as a trusted laptop identity")
				}
			}
		})
	}
}

func TestOldLocalPullPinRecoversOnlyOriginalSession(t *testing.T) {
	for _, test := range []struct {
		layers []api.Layer
		want   int64
	}{
		{layers: []api.Layer{pullLayer(api.LayerProfile, "work", "1G"), {Kind: api.LayerSession}}, want: api.PullMaxDefault},
		{layers: []api.Layer{pullLayer(api.LayerSession, "", "1G")}, want: 1 << 30},
	} {
		pin, err := api.PinPolicySources(test.layers)
		if err != nil {
			t.Fatal(err)
		}
		limit, err := pullMaxFromPin(pin)
		if err != nil || limit != test.want {
			t.Fatalf("old local pin ceiling = %d, %v; want %d", limit, err, test.want)
		}
	}
}

func pullLayer(kind api.LayerKind, name, value string) api.Layer {
	return api.Layer{Kind: kind, Name: name, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{PullMax: &value}}}
}
func pullLayerPtr(kind api.LayerKind, name, value string) *api.Layer {
	layer := pullLayer(kind, name, value)
	return &layer
}
