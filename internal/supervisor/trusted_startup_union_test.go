package supervisor

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
)

func TestCompleteStartupUnionPreservesSeparateTrustedAndGuestDomains(t *testing.T) {
	intent := trustedControlIntent()
	plan := &startupSecretInput{Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{}, Binds: map[string]api.SecretBind{}}, Values: map[string]api.Secret{}}
	if err := plan.addTrustedProcess(t.Context(), intent, map[string]api.Secret{"KEY": "ABCD0000"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := plan.addTrustedProcess(t.Context(), intent, map[string]api.Secret{"KEY": "ABCD0000"}, map[string]api.Secret{"KEY": "ABCD0000"}); err != nil {
		t.Fatal(err)
	}
	if len(plan.Intent.Modes) != 0 || len(plan.Intent.Binds) != 0 || len(plan.Values) != 0 || len(plan.SourceKinds) != 0 || len(plan.Processes) != 0 || len(plan.ProtectedTrustedSecretValues) != 1 {
		t.Fatal("trusted union changed a guest name/value/provenance domain")
	}
	role := plan.Intent.TrustedUses["KEY"]
	role.Bind.Hosts[0] = "changed.example:443"
	plan.Intent.TrustedUses["KEY"] = role
	if intent.TrustedUses["KEY"].Bind.Hosts[0] != "judge.example:443" {
		t.Fatal("union borrowed original protected role binding")
	}
}

func TestCompleteStartupUnionRejectsConflictingValuesAndSameNameRoleTuples(t *testing.T) {
	for _, variant := range []string{"value", "guest", "endpoint", "extra"} {
		t.Run(variant, func(t *testing.T) {
			intent := trustedControlIntent()
			plan := &startupSecretInput{Intent: api.SecretDeliveryIntent{Version: 1}, Values: map[string]api.Secret{}}
			if err := plan.addTrustedProcess(t.Context(), intent, map[string]api.Secret{"KEY": "ABCD0000"}, nil); err != nil {
				t.Fatal(err)
			}
			values := map[string]api.Secret{"KEY": "ABCD0000"}
			var guest map[string]api.Secret
			switch variant {
			case "value":
				values["KEY"] = "WXYZ1111"
			case "guest":
				guest = map[string]api.Secret{"KEY": "WXYZ1111"}
			case "endpoint":
				use := intent.TrustedUses["KEY"]
				use.Endpoint = "https://other.example/v1/chat/completions"
				use.Bind.Hosts = []string{"other.example:443"}
				intent.TrustedUses["KEY"] = use
			case "extra":
				values["EXTRA"] = "ABCD0000"
			}
			if err := plan.addTrustedProcess(t.Context(), intent, values, guest); err == nil {
				t.Fatal("complete original bundle accepted a conflicting protected role/value")
			}
		})
	}
}
