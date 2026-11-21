package egress

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestTrustedReadinessComparesTheOriginalTupleBeforeArmedState(t *testing.T) {
	args := trustedRoleArgs(t, true)
	server, initial := newBoundRegistryServer(t, args)
	query := api.TrustedSecretReadinessQueryV1{Version: 1, Generation: initial.Generation, Uses: api.CloneTrustedSecretUses(args.TrustedUses)}
	ready, err := server.trustedSecretReadiness(memberJSON(t, query))
	if err != nil || !ready.ValidFor(query) || len(ready.Unarmed) != 0 {
		t.Fatal("actual same-role readiness was unavailable", err)
	}
	changed := *api.CloneTrustedSecretReadinessQueryV1(&query)
	use := changed.Uses["KEY"]
	use.Endpoint = "https://other.example/v1/chat/completions"
	changed.Uses["KEY"] = use
	if _, err := server.trustedSecretReadiness(memberJSON(t, changed)); err == nil {
		t.Fatal("same name with a different endpoint obtained armed readiness")
	}
	armed, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.trustedSecretReadiness(memberJSON(t, query)); err == nil {
		t.Fatal("readiness survived an intervening actual guest rotation")
	}
	query.Generation = armed.Generation
	ready, err = server.trustedSecretReadiness(memberJSON(t, query))
	if err != nil || !ready.ValidFor(query) || len(ready.Unarmed) != 1 || ready.Unarmed[0] != "KEY" {
		t.Fatal("fresh actual role readiness did not expose its unarmed name", err)
	}
}

func TestTrustedReadinessMarksAnExplicitCurrentRoleChangeUnarmedWithoutRegisteringIt(t *testing.T) {
	args := trustedRoleArgs(t, false)
	server, initial := newBoundRegistryServer(t, args)
	policy := api.ClonePolicy(args.Initial.Policy)
	policy.Classifier.Key = new("NEXT")
	policy.Classifier.Endpoint = new("https://next.example/v1/chat/completions")
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	use := args.TrustedUses["KEY"]
	use.Endpoint = *policy.Classifier.Endpoint
	use.Bind.Hosts = []string{"next.example:443"}
	query := api.TrustedSecretReadinessQueryV1{Version: 1, Generation: initial.Generation, Uses: map[string]api.TrustedSecretUseV1{"NEXT": use}}
	before := server.currentMembers()
	ready, err := server.trustedSecretReadiness(memberJSON(t, query))
	if err != nil || !ready.ValidFor(query) || len(ready.Unarmed) != 1 || ready.Unarmed[0] != "NEXT" {
		t.Fatal("explicit current desired role could not reach full original arm gates", err)
	}
	if server.currentMembers() != before {
		t.Fatal("negative readiness registered an unfetched role")
	}
	if _, exists := server.currentMembers().secrets["NEXT"]; exists {
		t.Fatal("metadata query created registry authority")
	}
	_ = server.Close()
	if _, err := server.trustedSecretReadiness(memberJSON(t, query)); err == nil {
		t.Fatal("closed cohort supplied readiness")
	}
}
