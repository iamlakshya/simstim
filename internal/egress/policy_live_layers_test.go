package egress

import (
	"reflect"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestAppliedLayerProjectionPreventsOrdinaryReMergeFromApplyingNextStartValues(t *testing.T) {
	_, applied, err := MergeLayers([]api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: new("process"), Image: new("old"), Secrets: []string{"OLD"}}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := api.PolicyLaptopLayersV1(applied)
	if err != nil {
		t.Fatal(err)
	}
	updates[0].Sandbox.Wall, updates[0].Sandbox.Image, updates[0].Sandbox.Secrets = new("microvm"), new("new"), []string{"NEXT"}
	updates[0].Network.Deny = []string{"blocked.example"}
	desired, err := api.ReplacePolicyLaptopLayersV1(applied, updates)
	if err != nil {
		t.Fatal(err)
	}
	_, desired, err = MergeLayers(desired)
	if err != nil {
		t.Fatal(err)
	}
	live, next, err := ProjectLivePolicyLayersV1(applied, desired)
	if err != nil || !slices.Contains(next, "sandbox.secrets") || !slices.Contains(next, "sandbox.image") || !slices.Contains(next, "sandbox.wall") {
		t.Fatal("next-start source changes were not retained separately", next, err)
	}
	merged, _, err := MergeLayers(live)
	if err != nil || *merged.Sandbox.Wall != "process" || *merged.Sandbox.Image != "old" || !reflect.DeepEqual(merged.Sandbox.Secrets, []string{"OLD"}) || len(merged.Rules) != 1 || merged.Rules[0].Target != "blocked.example" || merged.Rules[0].Allow {
		t.Fatal("an ordinary source re-merge armed pending settings or lost a live deny", err)
	}
	desired[len(desired)-1].Rules = []api.Rule{{Layer: api.LayerSession, Door: api.DoorNet, Target: "forged.example", Allow: true}}
	if _, _, err := ProjectLivePolicyLayersV1(applied, desired); err == nil {
		t.Fatal("projection accepted a replacement of the protected session layer")
	}
}
