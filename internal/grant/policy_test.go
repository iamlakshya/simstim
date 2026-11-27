package grant

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCheckExplainsTheCanonicalDecidingLayer(t *testing.T) {
	policy, _, err := MergeLayers([]api.Layer{
		{Kind: api.LayerAll, Rules: []api.Rule{{Target: "api.example.com"}}},
		{Kind: api.LayerRepo, Name: "forge.example/team/*", Rules: []api.Rule{{Target: "api.example.com"}}},
		{Kind: api.LayerRepo, Name: "forge.example/team/app", Rules: []api.Rule{{Allow: true, Target: "api.example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	explanation, err := Decide("", policy, "api.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !explanation.Allow || explanation.Layer != api.LayerRepo || explanation.Name != "forge.example/team/app" || explanation.Rule == "" {
		t.Fatalf("explanation=%+v", explanation)
	}
}

func TestCheckHostLeavesPrivateNetworkAuthorityToClient(t *testing.T) {
	for _, target := range []string{netip.AddrFrom4([4]byte{192, 168, 1, 2}).String() + ":5432", "10/8", "[::1]:5432"} {
		canonical, private, err := CheckHost(target)
		if err != nil || !private || canonical == "" {
			t.Fatalf("%s = %q %v %v", target, canonical, private, err)
		}
	}
	if _, private, err := CheckHost("api.example.com"); err != nil || private {
		t.Fatalf("public host: private=%v err=%v", private, err)
	}
}

func TestAdditionPreviewUsesCanonicalReplacementAndSessionCollision(t *testing.T) {
	original := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "session-collision-7608.example", Once: true}
	original.ID = api.RuleID(original)
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Rules: []api.Rule{original}}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckAddition(policy, api.Rule{Allow: true, Door: api.DoorNet, Target: original.Target}); err != nil {
		t.Fatalf("preview refused the same canonical replacement as a real addition: %v", err)
	}
	collision := api.Rule{Allow: true, Door: api.DoorNet, Target: "session-collision-21481.example"}
	collision.Layer = api.LayerSession
	if api.RuleID(collision) != original.ID {
		t.Fatal("the real session collision fixture changed")
	}
	if err := CheckAddition(policy, collision); err == nil {
		t.Fatal("preview accepted an addition that collides in the actual session layer")
	}
	after, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("addition preview mutated the original policy or once lease")
	}
}
