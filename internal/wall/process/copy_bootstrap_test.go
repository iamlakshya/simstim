package process

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOriginalStageRolesCannotAliasOrDisappear(t *testing.T) {
	name := ".copy-0123456789abcdef0123456789abcdef"
	original := Request{V: 2, Mounts: []MountRequest{{FD: 7, Guest: "/work", Write: true, StageFD: 8, StageName: name}}}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRequest(strings.NewReader(string(raw)))
	if err != nil || got.Mounts[0].StageFD != 8 || got.Mounts[0].StageName != name {
		t.Fatalf("actual V2 stage round trip: %#v %v", got, err)
	}
	for _, bad := range []string{
		`{"v":2,"probe":false,"dns":false,"mounts":[{"fd":7,"guest":"/work","write":true}]}`,
		`{"v":1,"probe":false,"dns":false,"mounts":[{"fd":6,"guest":"/work","write":true,"stage_fd":0,"stage_name":""}]}`,
		strings.Replace(string(raw), `"stage_fd":8`, `"stage_fd":7`, 1),
		strings.Replace(string(raw), `"stage_fd":8`, `"stage_fd":6`, 1),
		strings.Replace(string(raw), name, "../foreign", 1),
	} {
		if _, err := DecodeRequest(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted missing or aliased native stage roles: %s", bad)
		}
	}
	legacy := Request{V: 1, Mounts: []MountRequest{{FD: 6, Guest: "/work", Write: true}}}
	raw, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "stage_") {
		t.Fatal("V1 projection gained a private V2 role")
	}
	if _, err := DecodeRequest(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
}
