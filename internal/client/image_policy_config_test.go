package client

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestImagePolicyUsesCanonicalNestedConfigSchema(t *testing.T) {
	var settings Settings
	if _, err := decodeConfig(`[sandbox.image_policy]
identity = "release@example.com"
issuer = "https://issuer.example"
trusted_root = ""
`, &settings); err != nil {
		t.Fatal(err)
	}
	policy := settings.Sandbox.ImagePolicy
	if policy == nil || policy.Identity == nil || *policy.Identity != "release@example.com" || policy.Issuer == nil || *policy.Issuer != "https://issuer.example" || policy.TrustedRoot == nil || *policy.TrustedRoot != "" {
		t.Fatalf("nested policy lost explicit values: %+v", policy)
	}
	clone := api.CloneLayerValues(settings.LayerValues)
	*clone.Sandbox.ImagePolicy.Identity = "changed"
	if *policy.Identity != "release@example.com" {
		t.Fatal("config copy aliases the retained policy")
	}
	view := InspectConfigTable(map[string]any{"identity": "value", "issuer": "value", "trusted_root": ""}, reflect.TypeFor[api.ImagePolicyValues]())
	for _, key := range view.Keys {
		if key.Type == nil || key.Conflict != "" {
			t.Fatalf("config checker rejects policy leaf: %+v", key)
		}
	}
}

func TestImagePolicyCannotSilentlyDisableHalfConfiguredSignatureRequirement(t *testing.T) {
	identity := "release@example.com"
	for _, policy := range []*api.ImagePolicyValues{{Identity: &identity}, {Issuer: &identity}} {
		if validateImagePolicy(policy) == nil {
			t.Fatal("incomplete certificate policy disabled its signature requirement")
		}
	}
}
