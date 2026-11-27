package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestLocalSourcePinContainsOnlySelectedIdentitiesAndOriginalSessionValues(t *testing.T) {
	profile := api.Layer{Kind: api.LayerProfile, Name: "chosen-profile"}
	profile.Sandbox.Secrets = []string{"PROFILE_NAME_ONLY"}
	session := api.Layer{Kind: api.LayerSession}
	session.Sandbox.Secrets = []string{"SESSION_NAME_ONLY"}
	layers := []api.Layer{{Kind: api.LayerAll}, profile, session}
	raw, err := encodeLocalPolicySources(NewArgs{Layers: layers})
	if err != nil {
		t.Fatal(err)
	}
	layers[2].Sandbox.Secrets[0] = "changed-after-materialisation"
	pin, err := api.DecodePolicySourcePin(raw)
	if err != nil || pin == nil || len(pin.Layers) != 3 || len(pin.Session.Sandbox.Secrets) != 1 || pin.Session.Sandbox.Secrets[0] != "SESSION_NAME_ONLY" {
		t.Fatalf("local creation pin = %+v, %v", pin, err)
	}
	if bytes.Contains(raw, []byte("PROFILE_NAME_ONLY")) {
		t.Fatal("the pin materialised a laptop profile instead of retaining its identity")
	}
	if _, err := encodeLocalPolicySources(NewArgs{Layers: []api.Layer{{Kind: api.LayerRepo, Source: api.SourceRepositoryFile, Name: "host-selected"}, session}}); err == nil {
		t.Fatal("a host/repository-file source became local creation authority")
	}
	if absent, err := encodeLocalPolicySources(NewArgs{}); err != nil || absent != nil {
		t.Fatal("a missing source pin was manufactured")
	}
}

func TestOpaqueSourcePinRetainsExactBytesAndCannotChangeOnRetry(t *testing.T) {
	h := h5GrantState(t)
	p := h5Policy()
	p.PolicySources = json.RawMessage(`{ "layers" : [], "future" : { "n" : 1.00 } }`)
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || got == nil || !bytes.Equal(got.PolicySources, p.PolicySources) {
		t.Fatalf("opaque source bytes changed on initial reservation: %+v, %v", got, err)
	}
	if err := reserveSandbox(t.Context(), h, p); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []json.RawMessage{nil, json.RawMessage(`{"layers":[]}`), json.RawMessage(`{"layers":[],"extra":true}`)} {
		retry := p
		retry.PolicySources = changed
		var refusal *UserError
		if err := reserveSandbox(t.Context(), h, retry); !errors.As(err, &refusal) || refusal.Code != "policy_sources" {
			t.Fatalf("retry replaced immutable source authority: %v", err)
		}
	}
	got, err = LoadSandboxPolicy(t.Context(), h, p.SessionID)
	if err != nil || !bytes.Equal(got.PolicySources, p.PolicySources) {
		t.Fatal("a refused retry changed the opaque metadata")
	}
	// Tier/creation decoding intentionally does not interpret this future
	// payload. The preview codec can refuse it without rewriting the state.
	if _, err := api.DecodePolicySourcePin(got.PolicySources); err == nil {
		t.Fatal("a codec silently discarded unsupported future pin metadata")
	}
}

func TestActualNewFrameAndLocalSourcePinShareOneSnapshot(t *testing.T) {
	h := h5GrantState(t)
	layers := []api.Layer{{Kind: api.LayerHost, Name: h.Name}, {Kind: api.LayerSession}}
	layers[1].Sandbox.Secrets = []string{"LOCAL_NAME"}
	c := replyingCreationConn(t, h, []string{"session.id"}, func(a NewArgs) {
		creation, err := LoadSandboxPolicy(t.Context(), h, a.ID)
		if err != nil || creation == nil {
			t.Fatalf("outgoing frame had no local source pin: %v", err)
		}
		pin, err := api.DecodePolicySourcePin(creation.PolicySources)
		if err != nil || pin == nil || len(a.Layers) != 2 || pin.Layers[0].Name != h.Name ||
			len(pin.Session.Sandbox.Secrets) != 1 || pin.Session.Sandbox.Secrets[0] != a.Layers[1].Sandbox.Secrets[0] {
			t.Fatalf("wire and local creation snapshots differ: %v", err)
		}
	})
	if _, err := c.NewSession(t.Context(), NewArgs{Layers: layers}); err != nil {
		t.Fatal(err)
	}
}
