package api

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSandboxSnapshotExactEmptyAndOwnedRoundtrip(t *testing.T) {
	empty := &SandboxSnapshot{Version: 1, Primary: "a2a2a2a2", Generation: strings.Repeat("a", 64), Members: []string{}}
	raw, _ := json.Marshal(empty)
	got, err := DecodeSandboxSnapshot(raw, empty.Primary, 4<<20)
	if err != nil || got.Members == nil || !bytes.Contains(raw, []byte(`"members":[]`)) {
		t.Fatalf("snapshot%+v err%v raw%s", got, err, raw)
	}
	empty.Members = []string{"b3b3b3b3"}
	copy := empty.Clone()
	copy.Members[0] = "c4c4c4c4"
	if empty.Members[0] != "b3b3b3b3" {
		t.Fatal("snapshot clone aliases declared IDs")
	}
}
func TestSandboxSnapshotRefusesAmbiguousOrIncompleteDeclarations(t *testing.T) {
	good := `{"version":1,"primary":"a2a2a2a2","generation":"` + strings.Repeat("a", 64) + `","members":["b3b3b3b3"]}`
	cases := []string{
		strings.Replace(good, `"version":1`, `"Version":1`, 1),
		strings.Replace(good, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(good, `"version":1`, `"version":2`, 1),
		strings.Replace(good, `"version":1`, `"version":1.0`, 1),
		strings.Replace(good, `["b3b3b3b3"]`, `null`, 1),
		strings.Replace(good, `["b3b3b3b3"]`, `["b3b3b3b3","b3b3b3b3"]`, 1),
		strings.Replace(good, `["b3b3b3b3"]`, `["c4c4c4c4","b3b3b3b3"]`, 1),
		strings.Replace(good, `["b3b3b3b3"]`, `["a2a2a2a2"]`, 1),
		strings.Replace(good, `"primary":"a2a2a2a2"`, `"primary":"../escape"`, 1),
		strings.TrimSuffix(good, "}") + `,"future":true}`,
		good + ` {}`,
	}
	for n, raw := range cases {
		if _, err := DecodeSandboxSnapshot([]byte(raw), "a2a2a2a2", 4<<20); err == nil {
			t.Fatalf("case%d accepted: %s", n, raw)
		}
	}
	if _, err := DecodeSandboxSnapshot([]byte(good), "c4c4c4c4", 4<<20); err == nil {
		t.Fatal("original foreign selector accepted")
	}
	if _, err := DecodeSandboxSnapshot([]byte(good), "a2a2a2a2", len(good)-1); err == nil {
		t.Fatal("existing byte bound ignored")
	}
}

func TestNestedSnapshotRequestCannotBypassCanonicalCodec(t *testing.T) {
	for _, snapshot := range []string{
		`{"version":1,"version":1,"primary":"a2a2a2a2","generation":"` + strings.Repeat("a", 64) + `","members":[]}`,
		`{"Version":1,"primary":"a2a2a2a2","generation":"` + strings.Repeat("a", 64) + `","members":[]}`,
		`{"version":1,"primary":"a2a2a2a2","generation":"` + strings.Repeat("a", 64) + `","members":[],"future":true}`,
	} {
		var request struct {
			Snapshot *SandboxSnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal([]byte(`{"snapshot":`+snapshot+`}`), &request); err == nil {
			t.Fatal("typed nested request lost ambiguous snapshot")
		}
	}
}
