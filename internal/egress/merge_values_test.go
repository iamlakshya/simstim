package egress

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func valuePointer[T any](value T) *T { return &value }

func TestLayerValuesListsLocksAndRepositoryNarrowing(t *testing.T) {
	layers := []api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"FIRST", "SECOND"}, Env: map[string]string{"MODE": "all", "KEEP": "yes"}, Wall: valuePointer("microvm")}}},
		{Kind: api.LayerRepo, Name: "forge.example/team/app", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Secrets: []string{"!FIRST", "THIRD"}, Env: map[string]string{"MODE": "repo"}, Wall: valuePointer("process")}}},
	}
	policy, _, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(policy.Sandbox.Secrets, []string{"SECOND", "THIRD"}) {
		t.Fatalf("secrets=%v", policy.Sandbox.Secrets)
	}
	if policy.Sandbox.Env["MODE"] != "repo" || policy.Sandbox.Env["KEEP"] != "yes" {
		t.Fatalf("env=%v", policy.Sandbox.Env)
	}
	if *policy.Sandbox.Wall != "microvm" {
		t.Fatalf("wall=%s", *policy.Sandbox.Wall)
	}
	layers[0].Lock = &api.Lock{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: valuePointer("media")}}}
	layers = append(layers, api.Layer{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Paste: valuePointer("any")}}})
	if _, _, err := MergeLayers(layers); err == nil {
		t.Fatal("session changed locked paste")
	}
	for _, values := range []api.LayerValues{
		{Sandbox: api.SandboxValues{Preset: valuePointer("open")}},
		{Network: api.NetworkValues{Unknown: valuePointer("allow")}},
		{Sandbox: api.SandboxValues{Secrets: []string{"TOKEN"}}},
		{Network: api.NetworkValues{Allow: []string{"api.example.com"}}},
	} {
		if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Source: api.SourceRepositoryFile, LayerValues: values}}); err == nil {
			t.Fatalf("repository widened with %+v", values)
		}
	}
	if _, _, err := MergeLayers([]api.Layer{{Kind: api.LayerRepo, Name: "forge.example/team/app", Source: api.SourceRepositoryFile, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Preset: valuePointer("locked"), Wall: valuePointer("microvm")}, Network: api.NetworkValues{Deny: []string{"api.example.com"}, Unknown: valuePointer("deny")}}}}); err != nil {
		t.Fatal(err)
	}
}
