package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
)

func TestLiveSourceReplacementCannotInventForgePushApproval(t *testing.T) {
	sources := []api.Layer{{Kind: api.LayerAll}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}}
	applied, canonical, err := MergeLayers(sources)
	if err != nil {
		t.Fatal(err)
	}
	proposed := api.CloneLayers(canonical)
	proposed[0].Rules = []api.Rule{{Door: api.DoorPush, Target: "github.com/team/repository", Allow: true, Mode: api.PushModePublic}}
	_, proposed, err = MergeLayers(proposed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PrepareLivePolicyV1(applied, proposed); err == nil {
		t.Fatal("source packet supplied a new forge/public push approval")
	}
	approved, approvedSources, err := MergeLayers(proposed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := PrepareLivePolicyV1(approved, approvedSources); err != nil {
		t.Fatal("unchanged existing push identity was lost", err)
	}
	changed := api.CloneLayers(approvedSources)
	changed[0].Rules[0].Mode = ""
	if _, _, _, err := PrepareLivePolicyV1(approved, changed); err == nil {
		t.Fatal("changed public acknowledgement retained authority")
	}
	removed := api.CloneLayers(approvedSources)
	removed[0].Rules = nil
	if _, _, _, err := PrepareLivePolicyV1(approved, removed); err != nil {
		t.Fatal("push authority could not be removed", err)
	}
	toggle := api.CloneLayers(canonical)
	toggle[0].Sandbox.Push = new(true)
	if _, _, _, err := PrepareLivePolicyV1(applied, toggle); err == nil {
		t.Fatal("source packet enabled new push authority")
	}
}
