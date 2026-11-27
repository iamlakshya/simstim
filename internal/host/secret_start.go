package host

import "github.com/iamlakshya/simstim/internal/api"

func initialSecretState(intent *api.SecretDeliveryIntent) *api.SessionSecretState {
	if intent == nil {
		return nil
	}
	return &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.CloneSecretDeliveryIntent(*intent)}
}

func prepareResumeSecretDelivery(m *Manifest, intent *api.SecretDeliveryIntent, values map[string]api.Secret) (*api.SecretDeliveryIntent, error) {
	if m.SecretState != nil && (!m.SecretState.Supported() || intent == nil) {
		return nil, UserError("secret_delivery_required", "this session needs its original explicit secret delivery intent", "Use a matching client and freshly approve the original names and modes before resuming.")
	}
	walled := m.Wall != nil && m.Wall.Level != "none"
	owned, _, err := PrepareSecretDelivery(intent, values, walled, append([]string{}, m.Secrets...))
	return owned, err
}
