package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidateAcknowledgedSessionSecretState checks an actual final publication
// ACK against original local intent and freshly supplied values. It reuses
// the full canonical public-state, value, prefix and alias checks. Minted
// records may have appeared after agent start; no CA or fake init result is
// constructed. Success confers no receipt, name, Source or fetch authority.
func ValidateAcknowledgedSessionSecretState(ctx context.Context, selectedKind string, intent api.SecretDeliveryIntent, values map[string]api.Secret, state *api.SessionSecretState) error {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return err
	}
	if intent.Version != api.SecretDeliveryIntentV1 {
		return registryIntentError()
	}
	owned := api.CloneSessionSecretState(state)
	if err := ValidateSavedPublicSessionState(owned); err != nil {
		return err
	}
	for name := range intent.Binds {
		if intent.Modes[name] != api.SecretModeBound {
			return registryIntentError()
		}
	}
	actuals := make(map[api.Secret]bool, len(values))
	for name, value := range values {
		if _, declared := intent.Modes[name]; !declared {
			return registryIntentError()
		}
		if err := checkRegistryValue(name, value); err != nil {
			return err
		}
		actuals[value] = true
	}
	aliases := make(map[initialAliasKey]api.PublicSecretState)
	for name, mode := range intent.Modes {
		if err := sentinelContext(ctx, "registry"); err != nil {
			return err
		}
		if !mode.Valid() || owned.Intent.Modes[name] != mode {
			return registryIntentError()
		}
		public := owned.Primary[name]
		if mode == api.SecretModeExposed {
			if _, present := values[name]; present && !public.Armed {
				return invalidPublicSessionState()
			}
			continue
		}
		binding, exists := intent.Binds[name]
		if !exists {
			return registryIntentError()
		}
		canonical, err := canonicalRegistryBinding(name, binding)
		if err != nil {
			return err
		}
		ackBinding, err := canonicalRegistryBinding(name, owned.Intent.Binds[name])
		if err != nil || !sameRegistryBinding(canonical, ackBinding) {
			return registryIntentError()
		}
		if value, supplied := values[name]; supplied {
			if err := validateFreshAcknowledgedBound(ctx, name, public, canonical, value, []string{selectedKind}, aliases); err != nil {
				return err
			}
		}
	}
	return checkAcknowledgedActualCollisions(ctx, owned, actuals)
}

func validateFreshAcknowledgedBound(ctx context.Context, name string, public api.PublicSecretState, binding api.SecretBind, value api.Secret, sources []string, aliases map[initialAliasKey]api.PublicSecretState) error {
	if err := ValidatePublicSecretValue(ctx, name, public, binding, value); err != nil {
		return err
	}
	canonical, err := canonicalRegistryBinding(name, binding)
	if err != nil {
		return err
	}
	bound, err := restoredBoundSecret(ctx, name, public, canonical)
	if err != nil {
		return err
	}
	probe := registeredSecret{mode: api.SecretModeBound, value: value, present: true, identity: public.Identity, bound: bound}
	probe.bound.sentinel = public.Sentinel
	for _, kind := range sources {
		if err := requireSelectedPrefix(kind, name, probe, value, true); err != nil {
			return err
		}
	}
	key, err := publicInitialAliasKey(canonical, public, value)
	if err != nil {
		return err
	}
	if other, exists := aliases[key]; exists && (other.Sentinel != public.Sentinel || other.Identity != public.Identity) {
		return invalidPublicSessionState()
	}
	aliases[key] = public
	return nil
}

func checkAcknowledgedActualCollisions(ctx context.Context, state *api.SessionSecretState, actuals map[api.Secret]bool) error {
	check := func(value api.Secret) error {
		if err := sentinelContext(ctx, "registry"); err != nil {
			return err
		}
		if actuals[value] {
			return invalidPublicSessionState()
		}
		return nil
	}
	for _, public := range state.Primary {
		if public.Mode == api.SecretModeBound {
			if err := check(public.Sentinel); err != nil {
				return err
			}
		}
	}
	for _, records := range state.Minted {
		for _, public := range records {
			if err := check(public.Sentinel); err != nil {
				return err
			}
		}
	}
	for _, records := range state.Retired {
		for _, public := range records {
			if err := check(public.Sentinel); err != nil {
				return err
			}
		}
	}
	return nil
}
