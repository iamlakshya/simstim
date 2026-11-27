package host

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRuntimeCodecPreservesOpaqueFieldsAndRefusesAmbiguousKnownFields(t *testing.T) {
	var runtime Runtime
	future := []byte(`{"kept":true}`)
	if err := json.Unmarshal([]byte(`{"socket":"socket","respawn_count":0,"future":{"kept":true}}`), &runtime); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(runtime.extra["future"], future) {
		t.Fatal("unknown runtime retention metadata was discarded")
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var again Runtime
	if err := json.Unmarshal(encoded, &again); err != nil || !bytes.Equal(again.extra["future"], future) {
		t.Fatal("runtime rewrite lost opaque retention metadata", err)
	}
	for _, raw := range []string{
		`{"holder_pid":1,"holder_pid":2,"socket":"socket","respawn_count":0}`,
		`{"holder_pid":1,"Holder_PID":2,"socket":"socket","respawn_count":0}`,
	} {
		if err := json.Unmarshal([]byte(raw), &runtime); err == nil {
			t.Fatal("ambiguous runtime fields became typed retention authority")
		}
	}
}

func TestRuntimeCloneOwnsRetentionAndOpaqueBytes(t *testing.T) {
	original := Runtime{Artefacts: []string{"original"}, extra: map[string]json.RawMessage{"future": json.RawMessage(`{"kept":true}`)}}
	copy := cloneRuntime(original)
	copy.Artefacts[0] = "replacement"
	copy.extra["future"][0] = '['
	copy.extra["another"] = json.RawMessage(`true`)
	if original.Artefacts[0] != "original" || string(original.extra["future"]) != `{"kept":true}` || len(original.extra) != 1 {
		t.Fatal("runtime clone exposed original retention/opaque backing storage")
	}
}

func TestRuntimeOpaqueMetadataCannotOverrideAnOmittedKnownField(t *testing.T) {
	runtime := Runtime{extra: map[string]json.RawMessage{"label": json.RawMessage(`"foreign"`), "future": json.RawMessage(`true`)}}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	var again Runtime
	if err := json.Unmarshal(encoded, &again); err != nil || again.Label != "" || string(again.extra["future"]) != "true" {
		t.Fatal("opaque data overrode an actual typed field omitted by omitempty", err)
	}
}
