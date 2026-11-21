package egress

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// includePending is used ONLY for prospective size admission. Such public
// stand-ins must never be returned to a guest or treated as acknowledged.
func (state *memberState) publicSessionProjection(ctx context.Context, includePending bool) (*api.SessionSecretState, error) {
	intent := api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)}
	intent.TrustedUses = state.publicTrustedUses()
	for name, secret := range state.secrets {
		if !secretHasGuestUse(secret) {
			continue
		}
		intent.Modes[name] = secret.mode
		if secret.bound != nil {
			binding := secret.bound.binding
			binding.Hosts = slices.Clone(binding.Hosts)
			intent.Binds[name] = binding
		}
	}
	var minted map[string][]api.PublicMintedSecretState
	if includePending {
		minted = make(map[string][]api.PublicMintedSecretState)
		for name, secret := range state.secrets {
			for _, mint := range secret.minted {
				public, err := publicMintedState(ctx, name, mint)
				if err != nil {
					return nil, err
				}
				minted[name] = append(minted[name], public)
			}
		}
	} else {
		var err error
		minted, err = state.publicMinted(ctx)
		if err != nil {
			return nil, err
		}
	}
	retired, err := publicRetiredPrimaries(ctx, state.retired)
	if err != nil {
		return nil, err
	}
	return &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: intent, Generation: state.generation, Primary: state.publicSecrets(slices.Sorted(maps.Keys(state.secrets))), Minted: minted, Retired: retired}, nil
}

func checkBoundPublicStateSize(ctx context.Context, state *memberState) error {
	public, err := state.publicSessionProjection(ctx, true)
	if err != nil {
		return err
	}
	return checkBoundControlResult(public)
}
