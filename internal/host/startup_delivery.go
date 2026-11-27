package host

import "github.com/iamlakshya/simstim/internal/api"

func initialDelivery(m *Manifest) *api.SecretDeliveryIntent {
	if m.SecretState == nil {
		return nil
	}
	copy := api.CloneSecretDeliveryIntent(m.SecretState.Intent)
	return &copy
}
