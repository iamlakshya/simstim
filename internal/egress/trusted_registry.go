package egress

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// Existing entries without a trusted role retain their original guest use.
// A judge-only role has neither a guest binding nor a delivered stand-in.
func secretHasGuestUse(secret registeredSecret) bool { return secret.trusted == nil || secret.guest }

func canonicalTrustedUse(name string, input api.TrustedSecretUseV1) (api.TrustedSecretUseV1, error) {
	if err := checkRegistryValue(name, ""); err != nil {
		return api.TrustedSecretUseV1{}, err
	}
	if name == "SSH_AUTH_SOCK" {
		return api.TrustedSecretUseV1{}, registryIntentError()
	}
	if input.Version != 1 || input.Use != api.TrustedJudgeUseV1 || input.Mode != api.SecretModeBound || input.Model == "" {
		return api.TrustedSecretUseV1{}, registryIntentError()
	}
	endpoint := newTrustedEndpointStreamV1(context.Background())
	_, err := endpoint.WriteString(input.Endpoint)
	if err != nil {
		return api.TrustedSecretUseV1{}, err
	}
	authority, err := endpoint.Finish()
	if err != nil {
		return api.TrustedSecretUseV1{}, registryIntentError()
	}
	binding, err := canonicalRegistryBinding(name, input.Bind)
	if err != nil || !slices.Contains(binding.Hosts, authority) {
		return api.TrustedSecretUseV1{}, registryIntentError()
	}
	input.Bind = binding
	return input, nil
}

func trustedUseMatchesPolicy(name string, use api.TrustedSecretUseV1, policy api.Policy) bool {
	settings := policy.Classifier
	return trustedJudgeSettings(policy) && settings.Judge != nil && *settings.Judge == "model" && settings.Key != nil && *settings.Key == name && settings.Model != nil && *settings.Model == use.Model && settings.Endpoint != nil && *settings.Endpoint == use.Endpoint
}

func (state *memberState) publicTrustedUses() map[string]api.TrustedSecretUseV1 {
	var uses map[string]api.TrustedSecretUseV1
	for name, secret := range state.secrets {
		if secret.trusted != nil {
			if uses == nil {
				uses = make(map[string]api.TrustedSecretUseV1)
			}
			uses[name] = *secret.trusted
		}
	}
	return api.CloneTrustedSecretUses(uses)
}

func prepareTrustedInitial(ctx context.Context, state *memberState, args BoundInitArgs) error {
	for name := range args.ProtectedTrustedSecretValues {
		if _, ok := args.TrustedUses[name]; !ok {
			return registryIntentError()
		}
	}
	for _, name := range slices.Sorted(maps.Keys(args.TrustedUses)) {
		if err := sentinelContext(ctx, name); err != nil {
			return err
		}
		use, err := canonicalTrustedUse(name, args.TrustedUses[name])
		if err != nil {
			return err
		}
		value, present := args.ProtectedTrustedSecretValues[name]
		if present && !trustedUseMatchesPolicy(name, use, state.metadata.Policy) {
			return registryIntentError()
		}
		if present {
			if err := checkRegistryValue(name, value); err != nil {
				return err
			}
			if !validBoundCredential(value) {
				return registryIntentError()
			}
			if err := checkMintedHTTPValue(name, use.Bind, value); err != nil {
				return err
			}
		}
		secret, guest := state.secrets[name]
		if guest {
			// Each role must have its own original approved name; a shared name
			// never uses one role's fetched value to arm the other implicitly.
			if secret.present != present || present && secret.value != value {
				return registryIntentError()
			}
		} else {
			identity, err := registryNonce()
			if err != nil {
				return err
			}
			secret = registeredSecret{mode: api.SecretModeBound, value: value, present: present, identity: identity, valueEpoch: identity}
		}
		secret.guest, secret.trusted, secret.trustedArmed = guest, &use, present
		state.secrets[name] = secret
	}
	return nil
}

// The original arm operation supplies exact role metadata separately from
// ordinary guest Values. Existing guest aliases still use applyBoundUpdates.
func applyTrustedArm(ctx context.Context, state *memberState, args ArmArgs) ([]string, error) {
	if len(args.TrustedUses) != len(args.ProtectedTrustedSecretValues) {
		return nil, registryIntentError()
	}
	updates := maps.Clone(args.Values)
	if updates == nil {
		updates = make(map[string]api.Secret)
	}
	var changed []string
	for _, name := range slices.Sorted(maps.Keys(args.TrustedUses)) {
		if err := sentinelContext(ctx, name); err != nil {
			return nil, err
		}
		value, exists := args.ProtectedTrustedSecretValues[name]
		if !exists {
			return nil, registryIntentError()
		}
		use, err := canonicalTrustedUse(name, args.TrustedUses[name])
		if err != nil {
			return nil, err
		}
		if !trustedUseMatchesPolicy(name, use, state.metadata.Policy) || !validBoundCredential(value) {
			return nil, registryIntentError()
		}
		if err := checkMintedHTTPValue(name, use.Bind, value); err != nil {
			return nil, err
		}
		if guestValue, present := args.Values[name]; present && guestValue != value {
			return nil, registryIntentError()
		}
		secret, exists := state.secrets[name]
		guest := exists && secretHasGuestUse(secret)
		if guest && secret.bound != nil {
			if !secret.present || secret.value != value {
				if guestValue, approved := args.Values[name]; !approved || guestValue != value {
					return nil, registryIntentError()
				}
			}
			updates[name] = value
		} else if guest && (!secret.present || secret.value != value) {
			return nil, registryIntentError()
		}
		roleChanged := secret.trusted == nil || !secret.trustedArmed || !reflect.DeepEqual(*secret.trusted, use)
		if !exists {
			identity, err := registryNonce()
			if err != nil {
				return nil, err
			}
			secret = registeredSecret{mode: api.SecretModeBound, identity: identity, valueEpoch: identity}
		}
		if !guest && (!secret.present || secret.value != value) {
			if registrySentinels(state)[value] {
				return nil, registryCollisionError(name)
			}
			if secret.present && !slices.Contains(secret.history, secret.value) {
				secret.history = append(secret.history, secret.value)
			}
			epoch, err := registryNonce()
			if err != nil {
				return nil, err
			}
			secret.value, secret.present, secret.valueEpoch = value, true, epoch
			changed = append(changed, name)
		}
		if roleChanged {
			changed = append(changed, name)
		}
		secret.guest, secret.trusted = guest, &use
		state.secrets[name] = secret
	}
	for name := range args.ProtectedTrustedSecretValues {
		if _, ok := args.TrustedUses[name]; !ok {
			return nil, registryIntentError()
		}
	}
	guestChanged, err := applyBoundUpdates(ctx, state, updates)
	if err != nil {
		return nil, err
	}
	for name := range args.TrustedUses {
		secret := state.secrets[name]
		if !secret.present || secret.value != args.ProtectedTrustedSecretValues[name] {
			return nil, registryIntentError()
		}
		secret.trustedArmed = true
		state.secrets[name] = secret
	}
	changed = append(changed, guestChanged...)
	slices.Sort(changed)
	return slices.Compact(changed), nil
}

func sameTrustedUseMetadata(left, right map[string]api.TrustedSecretUseV1) bool {
	if len(left) != len(right) {
		return false
	}
	for name, use := range left {
		other, exists := right[name]
		if !exists {
			return false
		}
		canonical, err := canonicalTrustedUse(name, use)
		if err != nil {
			return false
		}
		checked, err := canonicalTrustedUse(name, other)
		if err != nil || !reflect.DeepEqual(canonical, checked) {
			return false
		}
	}
	return true
}

// These helpers validate public metadata only. They create no request,
// Source, permission, value lookup, fetch or registry entry.
func ValidateTrustedSecretUseV1(name string, use api.TrustedSecretUseV1) error {
	_, err := canonicalTrustedUse(name, use)
	return err
}
func EqualTrustedSecretUsesV1(left, right map[string]api.TrustedSecretUseV1) bool {
	return sameTrustedUseMetadata(left, right)
}

func trustedAddedAuthorities(current, next *memberState, names []string) []string {
	var authorities []string
	for _, name := range names {
		secret := next.secrets[name]
		if secret.trusted == nil {
			continue
		}
		old := current.secrets[name]
		for _, authority := range secret.trusted.Bind.Hosts {
			if old.trusted == nil || !slices.Contains(old.trusted.Bind.Hosts, authority) {
				authorities = append(authorities, authority)
			}
		}
	}
	slices.Sort(authorities)
	return slices.Compact(authorities)
}
