package supervisor

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// A warm resume with no fresh trusted value keeps the existing role. Matching
// public metadata only narrows that existing role; it cannot arm or fetch it.
func warmTrustedDeliveryV1(delivery *api.SecretDeliveryIntent, protected map[string]api.Secret, current *api.SessionSecretState) (*api.SecretDeliveryIntent, error) {
	if delivery == nil || len(delivery.TrustedUses) == 0 || len(protected) > 0 {
		return delivery, nil
	}
	if current == nil {
		return nil, secretIntentFailure()
	}
	for name, use := range delivery.TrustedUses {
		prior, present := current.Intent.TrustedUses[name]
		if !present || !egress.EqualTrustedSecretUsesV1(map[string]api.TrustedSecretUseV1{name: use}, map[string]api.TrustedSecretUseV1{name: prior}) {
			return nil, secretIntentFailure()
		}
	}
	owned := api.CloneSecretDeliveryIntent(*delivery)
	owned.TrustedUses = nil
	return &owned, nil
}
