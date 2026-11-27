package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestPolicyReplacementDecodeRefusesNestedAliasesAndProtectedSources(t *testing.T) {
	valid := api.PolicyReplaceArgsV1{Version: 1, Session: "root2345", PreviousHash: strings.Repeat("a", 64), Layers: []api.Layer{{Kind: api.LayerAll}}}
	raw, _ := json.Marshal(valid)
	if _, err := DecodePolicyReplace(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"kind":"all"`, `"kind":"all","Kind":"all"`, 1),
		strings.Replace(string(raw), `"kind":"all"`, `"kind":"all","kind":"all"`, 1),
		strings.Replace(string(raw), `"kind":"all"`, `"kind":"session"`, 1),
		strings.Replace(string(raw), `"kind":"all"`, `"kind":"all","future":true`, 1),
	} {
		if _, err := DecodePolicyReplace([]byte(bad)); err == nil {
			t.Fatal("unsupported source packet reached the replacement producer")
		}
	}
}

func TestPolicySnapshotSeparatesAppliedValuesAndPendingSourceHash(t *testing.T) {
	level, pendingLevel := "process", "microvm"
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &level}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}}
	_, layers, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	laptop, _ := api.PolicyLaptopLayersV1(layers)
	laptop[0].Sandbox.Wall = &pendingLevel
	raw, err := api.EncodePolicyNextLayersV1(laptop)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := PolicySnapshotWithNext(layers, raw)
	want, _ := api.HashPolicyLaptopLayersV1(laptop)
	if err != nil || policy.Sandbox.Wall == nil || *policy.Sandbox.Wall != level || policy.LayersHash != want || len(policy.NextLayers) != 2 || len(policy.NextStart) != 1 || policy.NextStart[0] != "sandbox.wall" {
		t.Fatalf("applied and pending policy mixed: %+v, %v", policy, err)
	}
	*policy.NextLayers[0].Sandbox.Wall = "none"
	if *layers[0].Sandbox.Wall != level {
		t.Fatal("pending view changed the applied source")
	}
}
