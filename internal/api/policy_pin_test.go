package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPolicyPinKeepsTypedRefsAndOriginalSessionValues(t *testing.T) {
	secrets := []string{"!INHERITED", "EXPLICIT"}
	layers := []Layer{{Kind: LayerAll}, {Kind: LayerHost, Name: "box"}, {Kind: LayerRepo, Name: "forge.example/team/app"}, {Kind: LayerProfile, Name: "review"}, {Kind: LayerSession, LayerValues: LayerValues{Sandbox: SandboxValues{Secrets: secrets, Env: map[string]string{"MODE": "review"}}}}}
	pin, err := PinPolicySources(layers)
	if err != nil {
		t.Fatal(err)
	}
	secrets[0] = "CHANGED"
	layers[4].Sandbox.Env["MODE"] = "changed"
	if pin.Session.Sandbox.Secrets[0] != "!INHERITED" || pin.Session.Sandbox.Env["MODE"] != "review" {
		t.Fatal("creation pin aliases its caller")
	}
	b, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	read, err := DecodePolicySourcePin(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Layers) != 5 || read.Layers[2].Name != "forge.example/team/app" || read.Session.Sandbox.Secrets[0] != "!INHERITED" {
		t.Fatal("lost pinned identities or removals")
	}
}

func TestPolicyPinRejectsAmbiguousAndHostSourcedMetadata(t *testing.T) {
	for _, body := range []string{
		`{"layers":[{"kind":"session"}],"Layers":[],"session":{}}`,
		`{"layers":[{"kind":"session","Kind":"repo"}],"session":{}}`,
		`{"layers":[{"kind":"future"}],"session":{}}`,
		`{"layers":[{"kind":"repo","name":"forged","source":"repository-file"},{"kind":"session"}],"session":{}}`,
		`{"layers":[{"kind":"repo","name":""},{"kind":"session"}],"session":{}}`,
		`{"layers":[{"kind":"profile","name":"bad\nname"},{"kind":"session"}],"session":{}}`,
		`{"layers":[{"kind":"session"},{"kind":"session"}],"session":{}}`,
		`{"layers":[{"kind":"session"}],"session":{"sandbox":{"secrets":["X"],"Secrets":["Y"]}}}`,
		`{"layers":[{"kind":"session"}],"session":{"future":{"required":true}}}`,
		`{"layers":[{"kind":"session"}],"session":null}`,
	} {
		if _, err := DecodePolicySourcePin([]byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

// This input remains legal near the existing frame boundary. It must be in
// the H5 repeated-actor memory proof; no smaller hidden policy cap is used.
func maximumLegalPolicyPin(t testing.TB) []byte {
	t.Helper()
	pin := PolicySourcePin{Layers: []LayerRef{{Kind: LayerAll}, {Kind: LayerHost, Name: "box"}, {Kind: LayerSession}}, Session: LayerValues{Sandbox: SandboxValues{Env: map[string]string{"PAYLOAD": strings.Repeat("x", proto.MaxFrame-2048)}}}}
	b, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	if len(b)+1 > proto.MaxFrame {
		t.Fatal("maximum-legal fixture exceeds existing wire bound")
	}
	return b
}

func TestMaximumLegalPolicyPinIsNotSilentlyExcluded(t *testing.T) {
	if _, err := DecodePolicySourcePin(maximumLegalPolicyPin(t)); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkMaximumLegalPolicyPin(b *testing.B) {
	raw := maximumLegalPolicyPin(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for b.Loop() {
		if _, err := DecodePolicySourcePin(raw); err != nil {
			b.Fatal(err)
		}
	}
}
