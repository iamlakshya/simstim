package egress

import (
	"errors"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostSelectorScalarLayerPrecedenceAndNoGrantMutation(t *testing.T) {
	first := api.HostSelectorV1{Names: []string{"one", "two"}}
	last := api.HostSelectorV1{Names: []string{"three"}}
	layers := []api.Layer{
		{Kind: api.LayerRepo, Name: "forge.example/team/app", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Host: &first}}},
		{Kind: api.LayerProfile, Name: "work", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Host: &last}}},
	}
	policy, _, err := MergeLayers(layers)
	if err != nil || policy.Sandbox.Host == nil || !reflect.DeepEqual(policy.Sandbox.Host.Names, []string{"three"}) {
		t.Fatalf("an ordered scalar was merged as an additive list: %v", err)
	}
	last.Names[0] = "changed"
	if policy.Sandbox.Host.Names[0] != "three" || len(policy.Sandbox.Secrets) != 0 || len(policy.Rules) != 0 {
		t.Fatal("selection borrowed mutable names or created sandbox grants")
	}
}

func TestHostSelectorRefusesForbiddenLayerAndLock(t *testing.T) {
	value := api.LayerValues{Sandbox: api.SandboxValues{Host: &api.HostSelectorV1{Names: []string{"one"}}}}
	for _, layer := range []api.Layer{
		{Kind: api.LayerAll, LayerValues: value},
		{Kind: api.LayerHost, Name: "one", LayerValues: value},
		{Kind: api.LayerAll, Lock: &api.Lock{LayerValues: value}},
		{Kind: api.LayerRepo, Name: "forge.example/team/app", Source: api.SourceRepositoryFile, LayerValues: value},
	} {
		_, _, err := MergeLayers([]api.Layer{layer})
		var detail *proto.Error
		code := "host_rule_layer"
		if layer.Source == api.SourceRepositoryFile {
			code = "layer_key"
		}
		if !errors.As(err, &detail) || detail.Code != code {
			t.Fatalf("forbidden selector failed without its exact layer error: %v", err)
		}
	}
}
