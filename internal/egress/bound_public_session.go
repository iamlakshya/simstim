package egress

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidatePublicSessionState validates a prospective public persistence view
// and its real init.bound request/result envelopes. It uses no entropy and
// fetches, publishes and persists nothing. Caller-owned initial metadata and
// publicCA are the actual current session metadata and public certificate.
// A cold producer must still check its actual fresh result before guest start.
func ValidatePublicSessionState(initial InitArgs, state *api.SessionSecretState, publicCA string) error {
	if state == nil || !state.Supported() || !validRegistryNonce(state.Generation) || len(initial.Secrets) != 0 || publicCA == "" {
		return invalidPublicSessionState()
	}
	owned := api.CloneSessionSecretState(state)
	if len(owned.Intent.Modes) != len(owned.Primary) {
		return invalidPublicSessionState()
	}
	args := BoundInitArgs{Version: MemberRegistryV2, Initial: initial, Modes: owned.Intent.Modes, Binds: owned.Intent.Binds, Saved: make(map[string]api.PublicSecretState), SavedMinted: owned.Minted, SavedRetired: owned.Retired}
	args.Initial.Secrets = nil
	for name, mode := range owned.Intent.Modes {
		if mode == api.SecretModeBound {
			args.Saved[name] = owned.Primary[name]
		}
	}
	if err := checkBoundInitialFrame(args); err != nil {
		return err
	}
	retired, err := prepareRetiredPrimaries(context.Background(), args.Saved, owned.Retired, owned.Intent.Binds)
	if err != nil {
		return err
	}
	publicRetired, err := publicRetiredPrimaries(context.Background(), retired)
	if err != nil {
		return err
	}
	// Cold public results are unarmed, even when the saved live view was
	// armed. Final fresh randomness/certificate bytes are checked again by
	// the actual producer. No placeholder actual value is projected here.
	primary := api.ClonePublicSecretStates(owned.Primary)
	for name, public := range primary {
		public.Armed = false
		primary[name] = public
	}
	result := BoundInitResult{Version: MemberRegistryV2, CACert: publicCA, Generation: owned.Generation, Secrets: primary, Minted: owned.Minted, Retired: publicRetired}
	if err := checkPublicSessionFrames(args, result); err != nil {
		return err
	}
	if err := checkBoundControlResult(owned); err != nil {
		return err
	}
	cold := api.CloneSessionSecretState(owned)
	cold.Primary, cold.Retired = primary, publicRetired
	if err := checkBoundControlResult(cold); err != nil {
		return err
	}
	return validatePublicSessionEntries(owned)
}

func invalidPublicSessionState() error {
	return memberError("secret_state", "public secret state is unsupported or inconsistent", "Use matching helpers and the saved approved intent. Do not fall back to exposure.")
}

func checkPublicSessionFrames(args BoundInitArgs, result BoundInitResult) error {
	if err := checkBoundInitialFrame(args); err != nil {
		return err
	}
	return checkBoundControlResult(result)
}

func validatePublicSessionEntries(state *api.SessionSecretState) error {
	ctx := context.Background()
	aliases := make(map[api.Secret]string)
	identities := make(map[string]string)
	bindings := make(map[string]api.SecretBind)
	for _, name := range slices.Sorted(maps.Keys(state.Intent.Modes)) {
		mode := state.Intent.Modes[name]
		public, exists := state.Primary[name]
		if err := checkRegistryValue(name, ""); err != nil {
			return err
		}
		if !exists || !mode.Valid() || public.Mode != mode {
			return invalidPublicSessionState()
		}
		if mode == api.SecretModeExposed {
			_, hasBinding := state.Intent.Binds[name]
			if !publicExposedFacts(hasBinding, public.Sentinel == "", public.Shape == nil, public.Identity == "") {
				return invalidPublicSessionState()
			}
			continue
		}
		binding, exists := state.Intent.Binds[name]
		if !exists || !validRegistryNonce(public.Identity) {
			return invalidPublicSessionState()
		}
		canonical, err := canonicalRegistryBinding(name, binding)
		if err != nil {
			return err
		}
		bound, err := restoredBoundSecret(ctx, name, public, canonical)
		if err != nil {
			return err
		}
		plan, err := compileSentinelShape(ctx, name, bound.shape)
		if err != nil || !plan.variable {
			return invalidPublicSessionState()
		}
		if err := checkMintedHTTPValue(name, canonical, public.Sentinel); err != nil {
			return err
		}
		bindings[name] = canonical
		previous := aliases[public.Sentinel]
		if previous == "" {
			previous = identities[public.Identity]
		}
		if previous != "" {
			other := state.Primary[previous]
			if !publicAliasFacts(publicEntryPrimary, publicEntryPrimary, other.Armed, public.Armed, other.Sentinel == public.Sentinel, other.Identity == public.Identity, reflect.DeepEqual(other.Shape, public.Shape), sameRegistryBinding(bindings[previous], canonical)) {
				return invalidPublicSessionState()
			}
		}
		aliases[public.Sentinel], identities[public.Identity] = name, name
	}
	for name := range state.Intent.Binds {
		if state.Intent.Modes[name] != api.SecretModeBound {
			return invalidPublicSessionState()
		}
	}
	for _, name := range slices.Sorted(maps.Keys(state.Minted)) {
		if state.Intent.Modes[name] != api.SecretModeBound {
			return invalidPublicSessionState()
		}
		for _, public := range state.Minted[name] {
			if err := ValidatePublicMintedSecretState(ctx, name, public, bindings[name]); err != nil {
				return err
			}
			if aliases[public.Sentinel] != "" || identities[public.Identity] != "" {
				return invalidPublicSessionState()
			}
			aliases[public.Sentinel], identities[public.Identity] = name, name
		}
	}
	return validatePublicRetiredEntries(ctx, state, bindings)
}
