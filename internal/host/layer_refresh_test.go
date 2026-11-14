package host

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestResumeLayersKeepSavedNamesAndSessionRules(t *testing.T) {
	name := "forge.example/team/app"
	old := []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: name}, {Kind: api.LayerRepo, Name: name, Source: api.SourceRepositoryFile, Rules: []api.Rule{{Target: "blocked.example.com"}}}, {Kind: api.LayerProfile, Name: "review"}, {Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "api.example.com", GrantID: "saved-instance", Once: true}}}}
	m := &Manifest{Wall: &WallRecord{Level: "process"}, Layers: old}
	fresh := []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: name, LayerValues: api.LayerValues{Network: api.NetworkValues{Deny: []string{"other.example.com"}}}}, {Kind: api.LayerProfile, Name: "review"}}
	if err := ApplyResumeLayers(m, fresh); err != nil {
		t.Fatal(err)
	}
	var file, session bool
	for _, layer := range m.Layers {
		if layer.Source == api.SourceRepositoryFile {
			file = true
		}
		if layer.Kind == api.LayerSession {
			session = true
			if len(layer.Rules) != 1 || layer.Rules[0].GrantID != "saved-instance" || !layer.Rules[0].Once {
				t.Fatal("session grant changed")
			}
		}
	}
	if !file || !session {
		t.Fatal("trusted layers disappeared")
	}
	if len(old[2].Rules) != 0 {
		t.Fatal("refresh mutated the original layer slice")
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var reread Manifest
	if err := json.Unmarshal(b, &reread); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.Layers, reread.Layers) || !reflect.DeepEqual(m.Policy, reread.Policy) {
		t.Fatal("trusted manifest policy did not round trip")
	}
}

func TestResumeLayerFailureChangesNothing(t *testing.T) {
	for _, fresh := range [][]api.Layer{
		{{Kind: api.LayerSession}},
		{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: "new-origin"}},
		{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: "saved", Source: api.SourceRepositoryFile}},
		{{Kind: api.LayerAll}, {Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}},
	} {
		m := &Manifest{Wall: &WallRecord{Level: "process"}, Layers: []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerRepo, Name: "saved"}}}
		before, _ := json.Marshal(m)
		if err := ApplyResumeLayers(m, fresh); err == nil {
			t.Fatalf("accepted %+v", fresh)
		}
		after, _ := json.Marshal(m)
		if string(before) != string(after) {
			t.Fatal("failed validation changed manifest")
		}
	}
}
