package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestImagePolicyWireProjectionKeepsOriginalPathsAndCertificateLeaves(t *testing.T) {
	identity, issuer, path, locked := "release@example.com", "https://issuer.example", "/policy-root.json", "/locked-root.json"
	layers := []Layer{{Kind: LayerAll, LayerValues: LayerValues{Sandbox: SandboxValues{ImagePolicy: &ImagePolicyValues{Identity: &identity, Issuer: &issuer, TrustedRoot: &path}}}, Lock: &Lock{LayerValues: LayerValues{Sandbox: SandboxValues{ImagePolicy: &ImagePolicyValues{TrustedRoot: &locked}}}}}}
	projected := ProjectClientImagePolicy(layers)
	data, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "trusted_root") || strings.Contains(string(data), path) || strings.Contains(string(data), locked) {
		t.Fatalf("wire projection leaked a laptop root path: %s", data)
	}
	if !strings.Contains(string(data), identity) || !strings.Contains(string(data), issuer) {
		t.Fatal("wire projection removed certificate policy metadata")
	}
	if *layers[0].Sandbox.ImagePolicy.TrustedRoot != path || *layers[0].Lock.Sandbox.ImagePolicy.TrustedRoot != locked {
		t.Fatal("wire projection changed the original local source")
	}
	*projected[0].Sandbox.ImagePolicy.Identity = "changed"
	if identity != "release@example.com" {
		t.Fatal("wire projection aliases its original identity")
	}
}

func TestImagePolicyWireProjectionPreservesAbsentAndEmptyGroups(t *testing.T) {
	if ProjectClientImagePolicy(nil) != nil {
		t.Fatal("nil layers changed shape")
	}
	empty := ""
	layers := []Layer{{Kind: LayerAll}, {Kind: LayerHost, Name: "box", LayerValues: LayerValues{Sandbox: SandboxValues{ImagePolicy: &ImagePolicyValues{Identity: &empty}}}}}
	projected := ProjectClientImagePolicy(layers)
	if projected[0].Sandbox.ImagePolicy != nil {
		t.Fatal("absent policy group was created")
	}
	if projected[1].Sandbox.ImagePolicy == nil || projected[1].Sandbox.ImagePolicy.Identity == nil || *projected[1].Sandbox.ImagePolicy.Identity != "" {
		t.Fatal("explicit empty certificate leaf was erased")
	}
}
