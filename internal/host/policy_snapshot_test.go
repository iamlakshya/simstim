package host

import (
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPolicySourcesAreReadOnlyOwnedResponseData(t *testing.T) {
	mode := "keep"
	layers := []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{
		Sandbox: api.SandboxValues{System: &mode, Env: map[string]string{"MODE": "source"}},
	}}}
	m := &Manifest{Layers: layers}
	before, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := PolicySnapshot(api.CloneLayers(m.Layers))
	if err != nil || len(policy.Sources) != 1 {
		t.Fatalf("read snapshot omitted source layers: %+v, %v", policy, err)
	}
	policy.Sources[0].Sandbox.Env["MODE"] = "response"
	*policy.Sources[0].Sandbox.System = "reset"
	after, err := json.Marshal(m)
	if err != nil || string(before) != string(after) || m.Policy != nil {
		t.Fatalf("read response persisted or changed trusted policy: %v", err)
	}
	stored, _, err := DropOnceRules(m.Layers, m.CreatedAt)
	if err != nil || len(stored.Sources) != 0 {
		t.Fatal("read-only Sources leaked into a next-start stored policy")
	}
}
