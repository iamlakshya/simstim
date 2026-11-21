package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidateBoundInitialResult validates the actual reply before any guest
// start. It reuses the canonical metadata, source and value validators. It
// neither fetches a name nor creates entropy, state or publication authority.
func ValidateBoundInitialResult(ctx context.Context, args BoundInitArgs, result BoundInitResult) error {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return err
	}
	if args.Version != MemberRegistryV2 || result.Version != MemberRegistryV2 || result.CACert == "" {
		return invalidPublicSessionState()
	}
	sources, prefixes, err := validateStartupSources(ctx, args)
	if err != nil {
		return err
	}
	if !sameTrustedUseMetadata(args.TrustedUses, result.TrustedUses) {
		return registryIntentError()
	}
	state := &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: args.Modes, Binds: args.Binds, TrustedUses: args.TrustedUses}, Generation: result.Generation, Primary: result.Secrets, Minted: result.Minted, Retired: result.Retired}
	if err := ValidateSavedPublicSessionState(state); err != nil {
		return err
	}
	if err := checkPublicSessionFrames(args, result); err != nil {
		return err
	}
	if err := checkBoundControlResult(state); err != nil {
		return err
	}
	actuals := make(map[api.Secret]bool)
	for name, value := range args.Initial.Secrets {
		if _, declared := args.Modes[name]; !declared {
			return invalidPublicSessionState()
		}
		actuals[api.Secret(value)] = true
	}
	for name, value := range args.ProtectedTrustedSecretValues {
		if _, ok := args.TrustedUses[name]; !ok {
			return registryIntentError()
		}
		actuals[value] = true
	}
	prior := inputPublicStandIns(args)
	aliases := make(map[initialAliasKey]api.PublicSecretState)
	for name, mode := range args.Modes {
		public := result.Secrets[name]
		text, present := args.Initial.Secrets[name]
		if public.Armed != present {
			return invalidPublicSessionState()
		}
		if mode != api.SecretModeBound {
			continue
		}
		if actuals[public.Sentinel] || prior[public.Sentinel] {
			return invalidPublicSessionState()
		}
		if saved, exists := args.Saved[name]; exists {
			if !reflect.DeepEqual(saved.Shape, public.Shape) {
				return invalidPublicSessionState()
			}
		} else if !present || public.Shape == nil || public.Shape.Prefix != prefixes[name] {
			return invalidPublicSessionState()
		}
		if present {
			value := api.Secret(text)
			if err := validateFreshAcknowledgedBound(ctx, name, public, args.Binds[name], value, sources[name], aliases); err != nil {
				return err
			}
		} else if _, saved := args.Saved[name]; !saved {
			return invalidPublicSessionState()
		}
	}
	if err := checkAcknowledgedActualCollisions(ctx, state, actuals); err != nil {
		return err
	}

	expected, err := prepareRetiredPrimaries(ctx, args.Saved, args.SavedRetired, args.Binds)
	if err != nil {
		return err
	}
	retired, err := prepareRetiredPrimaries(ctx, nil, result.Retired, nil)
	if err != nil {
		return err
	}
	if !sameRetiredCollections(expected, retired) || !sameRestoredMinted(args.SavedMinted, result.Minted) {
		return invalidPublicSessionState()
	}
	return nil
}

type initialAliasKey struct {
	value          api.Secret
	binding, shape string
}

func (initialAliasKey) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func publicInitialAliasKey(binding api.SecretBind, public api.PublicSecretState, value api.Secret) (initialAliasKey, error) {
	b, err := json.Marshal(binding)
	if err != nil {
		return initialAliasKey{}, invalidPublicSessionState()
	}
	s, err := json.Marshal(public.Shape)
	if err != nil {
		return initialAliasKey{}, invalidPublicSessionState()
	}
	return initialAliasKey{value: value, binding: string(b), shape: string(s)}, nil
}

func inputPublicStandIns(args BoundInitArgs) map[api.Secret]bool {
	values := make(map[api.Secret]bool)
	for _, public := range args.Saved {
		values[public.Sentinel] = true
	}
	for _, records := range args.SavedMinted {
		for _, public := range records {
			values[public.Sentinel] = true
		}
	}
	for _, records := range args.SavedRetired {
		for _, public := range records {
			values[public.Sentinel] = true
		}
	}
	return values
}

func sameRetiredCollections(a, b map[string][]registeredRetired) bool {
	if len(a) != len(b) {
		return false
	}
	for name, records := range a {
		other := b[name]
		if len(records) != len(other) {
			return false
		}
		index := make(map[api.Secret]registeredRetired, len(other))
		for _, entry := range other {
			index[entry.sentinel] = entry
		}
		for _, entry := range records {
			if found, exists := index[entry.sentinel]; !exists || !sameRetiredPrimary(entry, found) {
				return false
			}
		}
	}
	return true
}

func sameRestoredMinted(a, b map[string][]PublicMintedSecretState) bool {
	count := func(m map[string][]PublicMintedSecretState) int {
		n := 0
		for _, records := range m {
			if len(records) > 0 {
				n++
			}
		}
		return n
	}
	if count(a) != count(b) {
		return false
	}
	for name, records := range a {
		other := b[name]
		if len(records) != len(other) {
			return false
		}
		index := make(map[api.Secret]PublicMintedSecretState, len(other))
		for _, entry := range other {
			index[entry.Sentinel] = entry
		}
		for _, entry := range records {
			found, exists := index[entry.Sentinel]
			if !exists || entry.Authority != found.Authority || entry.Rule != found.Rule || !reflect.DeepEqual(entry.Shape, found.Shape) {
				return false
			}
		}
	}
	return true
}
