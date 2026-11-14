package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

func validatePublicRetiredEntries(ctx context.Context, state *api.SessionSecretState, bindings map[string]api.SecretBind) error {
	known := make(map[api.Secret]registeredRetired)
	identities := make(map[string]registeredRetired)
	for name, public := range state.Primary {
		if public.Mode != api.SecretModeBound {
			continue
		}
		entry, err := validatedRetiredPrimary(ctx, name, api.RetiredPrimarySecretState{Sentinel: public.Sentinel, Shape: public.Shape, Identity: public.Identity, Binding: bindings[name]})
		if err != nil {
			return err
		}
		known[entry.sentinel], identities[entry.identity] = entry, entry
	}
	minted := make(map[api.Secret]bool)
	mintedIDs := make(map[string]bool)
	for _, records := range state.Minted {
		for _, public := range records {
			minted[public.Sentinel], mintedIDs[public.Identity] = true, true
		}
	}
	for name, records := range state.Retired {
		for _, public := range records {
			entry, err := validatedRetiredPrimary(ctx, name, public)
			if err != nil {
				return err
			}
			if minted[entry.sentinel] || mintedIDs[entry.identity] {
				return invalidPublicSessionState()
			}
			if old, exists := known[entry.sentinel]; exists && !sameRetiredPrimary(old, entry) {
				return invalidPublicSessionState()
			}
			if old, exists := identities[entry.identity]; exists && !sameRetiredPrimary(old, entry) {
				return invalidPublicSessionState()
			}
			known[entry.sentinel], identities[entry.identity] = entry, entry
		}
	}
	return nil
}
