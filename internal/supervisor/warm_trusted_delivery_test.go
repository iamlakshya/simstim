package supervisor

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestWarmTrustedRoleRemainsUnmodifiedWithoutFreshValues(t *testing.T) {
	intent := trustedControlIntent()
	state := &api.SessionSecretState{Version: 1, Intent: api.CloneSecretDeliveryIntent(intent)}
	selected, err := warmTrustedDeliveryV1(&intent, nil, state)
	if err != nil {
		t.Fatal(err)
	}
	if selected == &intent || len(selected.TrustedUses) != 0 || len(intent.TrustedUses) != 1 || len(state.Intent.TrustedUses) != 1 {
		t.Fatal("warm metadata armed or changed the installed trusted role")
	}
	for _, variant := range []string{"missing", "endpoint", "model", "binding"} {
		t.Run(variant, func(t *testing.T) {
			current := api.CloneSessionSecretState(state)
			changed := api.CloneSecretDeliveryIntent(intent)
			use := changed.TrustedUses["KEY"]
			switch variant {
			case "missing":
				current = nil
			case "endpoint":
				use.Endpoint = "https://other.example/v1/chat/completions"
			case "model":
				use.Model = "other-model"
			case "binding":
				use.Bind.Hosts = []string{"other.example:443"}
			}
			changed.TrustedUses["KEY"] = use
			if _, err := warmTrustedDeliveryV1(&changed, nil, current); err == nil {
				t.Fatal("warm metadata nominated an absent or changed trusted role")
			}
		})
	}
}
