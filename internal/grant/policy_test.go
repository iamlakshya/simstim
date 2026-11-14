package grant

import (
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
	for _, target := range []string{"192.168.1.2:5432", "10/8", "[::1]:5432"} {
		canonical, private, err := CheckHost(target)
		if err != nil || !private || canonical == "" {
			t.Fatalf("%s = %q %v %v", target, canonical, private, err)
		}
	}
	if _, private, err := CheckHost("api.example.com"); err != nil || private {
		t.Fatalf("public host: private=%v err=%v", private, err)
	}
}
