package egress

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// applyBoundUpdates is shared by arm and v2 membership. Every alias name is
// freshly present in Values before a cohort can change its shared identity.
func applyBoundUpdates(ctx context.Context, state *memberState, values map[string]api.Secret) ([]string, error) {
	groups := make(map[string]api.Secret)
	for _, name := range slices.Sorted(maps.Keys(values)) {
		secret, exists := state.secrets[name]
		if !exists || secret.mode != api.SecretModeBound || secret.bound == nil {
			return nil, memberError("secret_arm", "arm may change only existing bound names", "Explicitly resume or start for exposed values and different binding intent.")
		}
		value := values[name]
		if err := checkRegistryValue(name, value); err != nil {
			return nil, err
		}
		if secret.present && secret.value == value {
			continue
		}
		if old, present := groups[secret.identity]; present && old != value {
			return nil, aliasRotationError()
		}
		groups[secret.identity] = value
	}
	for identity, value := range groups {
		for name, secret := range state.secrets {
			if secret.bound != nil && secret.identity == identity {
				if approved, present := values[name]; !present || approved != value {
					return nil, aliasRotationError()
				}
			}
		}
	}
	epochs := make(map[string]string, len(groups))
	for identity := range groups {
		epoch, err := registryNonce()
		if err != nil {
			return nil, err
		}
		epochs[identity] = epoch
	}
	reserved := registrySentinels(state)
	var changed []string
	for _, name := range slices.Sorted(maps.Keys(values)) {
		secret := state.secrets[name]
		value := values[name]
		if secret.present && secret.value == value {
			continue
		}
		if !validBoundCredential(value) || reserved[value] {
			return nil, registryCollisionError(name)
		}
		if err := checkSentinelValue(ctx, name, value, secret.bound.shape); err != nil {
			return nil, memberError("secret_shape", "secret "+name+" changed shape; replacement did not occur", "Stop and resume for a fresh approved sentinel generation.")
		}
		if secret.present && !slices.Contains(secret.history, secret.value) {
			secret.history = append(secret.history, secret.value)
		}
		secret.value, secret.present = value, true
		if secret.trusted != nil {
			secret.trustedArmed = false
		}
		secret.valueEpoch = epochs[secret.identity]
		state.secrets[name] = secret
		changed = append(changed, name)
	}
	return changed, nil
}

func registrySentinels(state *memberState) map[api.Secret]bool {
	values := make(map[api.Secret]bool)
	for _, secret := range state.secrets {
		if secret.bound != nil && secret.bound.sentinel != "" {
			values[secret.bound.sentinel] = true
		}
		for _, mint := range secret.minted {
			values[mint.sentinel] = true
		}
	}
	for _, entries := range state.retired {
		for _, entry := range entries {
			values[entry.sentinel] = true
		}
	}
	return values
}

func aliasRotationError() error {
	return memberError("secret_alias", "a shared secret identity needs consistent fresh approval for every alias; replacement did not occur", "Approve the complete saved alias group or stop and resume for a fresh generation. No additional name was fetched.")
}

// A running member's environment and Store state cannot be injected again.
// Every live operation must keep the exact boot stand-in and binding, even
// when a different freshly approved member joins with a new alias.
func validateLiveBoundSentinels(current, next *memberState) error {
	for name, old := range current.secrets {
		if old.bound == nil {
			continue
		}
		updated, present := next.secrets[name]
		if !present || !sameAliasFormat(old, updated) || old.identity != updated.identity || old.bound.sentinel != updated.bound.sentinel {
			return memberError("secret_shape", "secret "+name+" cannot keep its live guest stand-in; replacement did not occur", "Stop and resume for a fresh approved sentinel generation.")
		}
	}
	return nil
}
