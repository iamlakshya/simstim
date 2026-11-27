package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

func validateRetiredNamespace(ctx context.Context, state *memberState) error {
	known := make(map[api.Secret]registeredRetired)
	identities := make(map[string]registeredRetired)
	minted := make(map[api.Secret]bool)
	mintedIDs := make(map[string]bool)
	for _, secret := range state.secrets {
		if secret.bound != nil {
			entry := registeredRetired{sentinel: secret.bound.sentinel, shape: secret.bound.shape, identity: secret.identity, rule: secret.bound.rule, binding: secret.bound.binding}
			known[entry.sentinel], identities[entry.identity] = entry, entry
		}
		for _, mint := range secret.minted {
			minted[mint.sentinel], mintedIDs[mint.identity] = true, true
		}
	}
	values := registryRealValues(state)
	for name, entries := range state.retired {
		for _, entry := range entries {
			if entry.sentinel == "" || values[entry.sentinel] || minted[entry.sentinel] || mintedIDs[entry.identity] {
				return registryCollisionError(name)
			}
			if err := checkSentinelValue(ctx, name, entry.sentinel, entry.shape); err != nil {
				return err
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
