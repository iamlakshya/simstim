package egress

import (
	"context"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

func registryRealValues(state *memberState) map[api.Secret]bool {
	values := make(map[api.Secret]bool)
	for _, secret := range state.secrets {
		if secret.present {
			values[secret.value] = true
		}
		for _, value := range secret.history {
			values[value] = true
		}
		for _, mint := range secret.minted {
			values[mint.value] = true
		}
	}
	return values
}

func registryReservedValues(state *memberState) map[api.Secret]bool {
	values := registryRealValues(state)
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

func assignInitialSentinels(ctx context.Context, state *memberState, saved map[string]api.PublicSecretState) error {
	reserved := registryReservedValues(state)
	names := slices.Sorted(maps.Keys(state.secrets))
	for _, name := range names {
		secret := state.secrets[name]
		if secret.bound == nil || secret.bound.sentinel != "" {
			continue
		}
		for _, previous := range names {
			other := state.secrets[previous]
			if !sameAliasFormat(secret, other) || other.bound.sentinel == "" || secret.present != other.present {
				continue
			}
			equal := secret.present && secret.value == other.value
			if !secret.present {
				equal = saved[name].Sentinel != "" && saved[name].Sentinel == saved[previous].Sentinel
			}
			if equal {
				secret.bound.sentinel, secret.identity, secret.valueEpoch = other.bound.sentinel, other.identity, other.valueEpoch
				break
			}
		}
		if secret.bound.sentinel == "" {
			var real *api.Secret
			if secret.present {
				real = &secret.value
			}
			sentinel, err := makeSentinel(ctx, name, real, secret.bound.shape, func(candidate api.Secret) bool { return !reserved[candidate] })
			if err != nil {
				return err
			}
			identity, err := registryNonce()
			if err != nil {
				return err
			}
			secret.bound.sentinel, secret.identity, secret.valueEpoch = sentinel, identity, identity
			reserved[sentinel] = true
		}
		state.secrets[name] = secret
	}
	return nil
}

func sameAliasFormat(a, b registeredSecret) bool {
	return a.mode == api.SecretModeBound && b.mode == a.mode && a.bound != nil && b.bound != nil &&
		sameRegistryBinding(a.bound.binding, b.bound.binding) && sameSecretShape(a.bound.shape, b.bound.shape) && a.bound.rule == b.bound.rule
}

func validateRegistryCollisions(ctx context.Context, state *memberState) error {
	values := registryRealValues(state)
	current := make(map[api.Secret]registeredSecret)
	priorPublic := make(map[api.Secret]bool)
	for _, entries := range state.retired {
		for _, entry := range entries {
			priorPublic[entry.sentinel] = true
		}
	}
	for name, secret := range state.secrets {
		if secret.bound != nil {
			sentinel := secret.bound.sentinel
			if sentinel == "" || values[sentinel] || priorPublic[sentinel] || secret.identity == "" {
				return registryCollisionError(name)
			}
			if err := checkSentinelValue(ctx, name, sentinel, secret.bound.shape); err != nil {
				return err
			}
			if old, exists := current[sentinel]; exists && (!sameAliasFormat(secret, old) || old.identity != secret.identity || old.present != secret.present || secret.present && old.value != secret.value) {
				return registryCollisionError(name)
			}
			current[sentinel] = secret
		}
	}
	for name, secret := range state.secrets {
		for _, mint := range secret.minted {
			if secret.bound == nil || mint.identity == "" || mint.sentinel == "" {
				return registryCollisionError(name)
			}
			if !slices.Contains(secret.bound.binding.Hosts, mint.authority) || values[mint.sentinel] || priorPublic[mint.sentinel] || mint.sentinel == mint.value {
				return registryCollisionError(name)
			}
			if mint.retired && (mint.ready || mint.value != "" || mint.publication != nil) || !mint.retired && (mint.value == "" || mint.publication == nil) {
				return registryCollisionError(name)
			}
			if err := checkSentinelValue(ctx, name, mint.sentinel, mint.shape); err != nil {
				return err
			}
			if _, used := current[mint.sentinel]; used {
				return registryCollisionError(name)
			}
			current[mint.sentinel] = registeredSecret{identity: secret.identity}
		}
	}
	return validateRetiredNamespace(ctx, state)
}

func registryCollisionError(name string) error {
	return memberError("secret_collision", "secret "+name+" conflicts with registered credential state; replacement did not occur", "Stop and resume for a fresh sentinel generation. Do not reuse a public stand-in as a real credential.")
}
