package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestImagePolicyOptionalGroupMergesAndClonesLeaves(t *testing.T) {
	identity, issuer, root, next := "release@example.com", "https://issuer.example", "root.json", "next@example.com"
	layers := []api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Identity: &identity, Issuer: &issuer, TrustedRoot: &root}}}},
		{Kind: api.LayerHost, Name: "box", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{}}}},
		{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Identity: &next}}}},
	}
	policy, _, err := MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	got := policy.Sandbox.ImagePolicy
	if got == nil || got.Identity == nil || *got.Identity != next || got.Issuer == nil || *got.Issuer != issuer || got.TrustedRoot == nil || *got.TrustedRoot != root {
		t.Fatalf("wrong merged policy: %+v", got)
	}
	*got.Identity = "changed"
	if next != "next@example.com" {
		t.Fatal("merged policy aliases its source identity")
	}
	cloned := api.CloneLayers(layers)
	*cloned[0].Sandbox.ImagePolicy.Issuer = "changed"
	if issuer != "https://issuer.example" {
		t.Fatal("layer clone aliases nested policy leaves")
	}
}

func TestImagePolicyLockedLeafMaterialisesWithoutAnyOrdinaryValue(t *testing.T) {
	issuer := "https://issuer.example"
	locked := api.Layer{Kind: api.LayerAll, Lock: &api.Lock{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Issuer: &issuer}}}}}
	policy, _, err := MergeLayers([]api.Layer{locked})
	if err != nil {
		t.Fatal(err)
	}
	if policy.Sandbox.ImagePolicy == nil || policy.Sandbox.ImagePolicy.Issuer == nil || *policy.Sandbox.ImagePolicy.Issuer != issuer {
		t.Fatal("lock-only policy leaf was dropped")
	}
	other := "https://other.example"
	_, _, err = MergeLayers([]api.Layer{locked, {Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Issuer: &other}}}}})
	if err == nil {
		t.Fatal("narrower issuer bypassed its leaf lock")
	}
}

func TestRepositoryFileCannotSelectImagePolicyButLocalRepositoryCan(t *testing.T) {
	identity := "release@example.com"
	layer := api.Layer{Kind: api.LayerRepo, Name: "forge.example/team/repo", LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Identity: &identity}}}}
	if _, _, err := MergeLayers([]api.Layer{layer}); err != nil {
		t.Fatalf("trusted repository policy: %v", err)
	}
	layer.Source = api.SourceRepositoryFile
	if _, _, err := MergeLayers([]api.Layer{layer}); err == nil {
		t.Fatal("untrusted repository selected its signer")
	}
}

func TestImagePolicyExplicitEmptyLeafDoesNotEraseSibling(t *testing.T) {
	identity, issuer, empty := "release@example.com", "https://issuer.example", ""
	policy, _, err := MergeLayers([]api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Identity: &identity, Issuer: &issuer}}}},
		{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{ImagePolicy: &api.ImagePolicyValues{Identity: &empty}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if *policy.Sandbox.ImagePolicy.Identity != "" || *policy.Sandbox.ImagePolicy.Issuer != issuer {
		t.Fatal("explicit empty policy leaf changed its sibling")
	}
}
