package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestLayerSnapshotCopiesEveryMutableSourceShape(t *testing.T) {
	preset := "locked"
	until := time.Now().Add(time.Hour)
	source := []Layer{{
		Kind: LayerAll,
		LayerValues: LayerValues{Sandbox: SandboxValues{
			Preset: &preset, Secrets: []string{"FIRST"}, Env: map[string]string{"MODE": "old"},
		}},
		Rules: []Rule{{Target: "api.example.com", Until: until}},
		Lock: &Lock{Deny: []string{"blocked.example.com"}, LayerValues: LayerValues{
			Sandbox: SandboxValues{Env: map[string]string{"LOCKED": "old"}},
		}},
	}}
	before, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	owned := CloneLayers(source)
	if owned[0].Rules[0].Until != until {
		t.Fatal("time instant or monotonic data changed during a value copy")
	}
	*owned[0].Sandbox.Preset = "open"
	owned[0].Sandbox.Secrets[0] = "SECOND"
	owned[0].Sandbox.Env["MODE"] = "new"
	owned[0].Rules[0].Target = "other.example.com"
	owned[0].Lock.Deny[0] = "other.example.com"
	owned[0].Lock.Sandbox.Env["LOCKED"] = "new"
	after, err := json.Marshal(source)
	if err != nil || string(before) != string(after) {
		t.Fatalf("a returned snapshot mutated trusted source data: %s, %v", after, err)
	}
	if CloneLayers(nil) != nil || !reflect.DeepEqual(CloneLayerValues(LayerValues{}), LayerValues{}) {
		t.Fatal("empty source shape changed")
	}
}
