package supervisor

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
)

func hasSavedSecretRecords(state *api.SessionSecretState) bool {
	return state != nil && (state.Generation != "" || len(state.Primary) != 0 || len(state.Minted) != 0 || len(state.Retired) != 0)
}

func sameRetiredPublic(a, b api.RetiredPrimarySecretState) bool {
	return a.Sentinel == b.Sentinel && a.Identity == b.Identity && reflect.DeepEqual(a.Shape, b.Shape) && sameSecretBinding(a.Binding, b.Binding)
}

func appendRetiredPublic(state *api.SessionSecretState, name string, entry api.RetiredPrimarySecretState) error {
	for _, known := range state.Retired[name] {
		if known.Sentinel == entry.Sentinel || known.Identity == entry.Identity {
			if !sameRetiredPublic(known, entry) {
				return secretIntentFailure()
			}
			return nil
		}
	}
	if state.Retired == nil {
		state.Retired = make(map[string][]api.RetiredPrimarySecretState)
	}
	owned := api.CloneRetiredPrimaryStates(map[string][]api.RetiredPrimarySecretState{name: {entry}})[name][0]
	state.Retired[name] = append(state.Retired[name], owned)
	return nil
}

func appendSavedMinted(state *api.SessionSecretState, name string, entry api.PublicMintedSecretState) error {
	for _, known := range state.Minted[name] {
		if known.Sentinel == entry.Sentinel || known.Identity == entry.Identity {
			if !samePublicMintedState(known, entry) {
				return secretIntentFailure()
			}
			return nil
		}
	}
	if state.Minted == nil {
		state.Minted = make(map[string][]api.PublicMintedSecretState)
	}
	state.Minted[name] = append(state.Minted[name], clonePublicMintedState(name, entry))
	return nil
}

// collectSavedSecretState consumes only canonical protected host records.
// Root current metadata wins. An older child's different public primary
// becomes an explicit historical tripwire, never a fabricated minted value.
func collectSavedSecretState(ctx context.Context, root host.Manifest, members []*host.Manifest) (*api.SessionSecretState, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !api.ValidSessionID(root.ID) || root.SandboxOf != "" {
		return nil, secretIntentFailure()
	}
	if err := knownSessionSecretState(root.SecretState); err != nil {
		return nil, err
	}
	var result *api.SessionSecretState
	rootNames := make(map[string]bool)
	if hasSavedSecretRecords(root.SecretState) {
		if err := egress.ValidateSavedPublicSessionState(root.SecretState); err != nil {
			return nil, err
		}
		result = api.CloneSessionSecretState(root.SecretState)
		initialiseSavedSecretMaps(result)
		for name := range result.Primary {
			rootNames[name] = true
		}
	}
	for _, member := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if member == nil || member.ID == root.ID || member.SandboxOf != root.ID || !api.ValidSessionID(member.ID) {
			return nil, secretIntentFailure()
		}
		if err := knownSessionSecretState(member.SecretState); err != nil {
			return nil, err
		}
		if !hasSavedSecretRecords(member.SecretState) {
			continue
		}
		if err := egress.ValidateSavedPublicSessionState(member.SecretState); err != nil {
			return nil, err
		}
		if result == nil {
			result = &api.SessionSecretState{Version: api.SessionSecretStateV1, Intent: api.SecretDeliveryIntent{Version: api.SecretDeliveryIntentV1, Modes: make(map[string]api.SecretMode), Binds: make(map[string]api.SecretBind)},
				Generation: member.SecretState.Generation, Primary: make(map[string]api.PublicSecretState)}
		}
		if err := mergeSavedMember(ctx, result, rootNames, member); err != nil {
			return nil, err
		}
	}
	if result == nil {
		return nil, nil
	}
	initialiseSavedSecretMaps(result)
	// Genuine legacy names have no public stand-in or binding to invent.
	// Their absent values stay missing, with no fetch or exposure performed.
	for _, manifest := range append([]*host.Manifest{&root}, members...) {
		if manifest.SecretState != nil {
			continue
		}
		for _, name := range manifest.Secrets {
			if _, declared := result.Intent.Modes[name]; !declared {
				result.Intent.Modes[name] = api.SecretModeExposed
				result.Primary[name] = api.PublicSecretState{Mode: api.SecretModeExposed}
			}
		}
	}
	for name, public := range result.Primary {
		public.Armed = false
		result.Primary[name] = public
	}
	if err := egress.ValidateSavedPublicSessionState(result); err != nil {
		return nil, err
	}
	return result, nil
}

func initialiseSavedSecretMaps(state *api.SessionSecretState) {
	if state.Intent.Modes == nil {
		state.Intent.Modes = make(map[string]api.SecretMode)
	}
	if state.Intent.Binds == nil {
		state.Intent.Binds = make(map[string]api.SecretBind)
	}
	if state.Primary == nil {
		state.Primary = make(map[string]api.PublicSecretState)
	}
}

func mergeSavedMember(ctx context.Context, result *api.SessionSecretState, rootNames map[string]bool, member *host.Manifest) error {
	input := member.SecretState
	for _, name := range slices.Sorted(maps.Keys(input.Primary)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !slices.Contains(member.Secrets, name) {
			return secretIntentFailure()
		}
		public := input.Primary[name]
		current, exists := result.Primary[name]
		if !exists {
			result.Intent.Modes[name] = input.Intent.Modes[name]
			if public.Mode == api.SecretModeBound {
				result.Intent.Binds[name] = api.CloneSecretBinds(map[string]api.SecretBind{name: input.Intent.Binds[name]})[name]
			}
			result.Primary[name] = api.ClonePublicSecretStates(map[string]api.PublicSecretState{name: public})[name]
			continue
		}
		compatible := sameLivePrimary(current, public) && sameSecretBinding(result.Intent.Binds[name], input.Intent.Binds[name])
		if !rootNames[name] && !compatible {
			// Two children cannot nominate a current winner without a
			// canonical root record. Their differing metadata is ambiguous.
			return secretIntentFailure()
		}
		if public.Mode == api.SecretModeBound && !compatible {
			entry := api.RetiredPrimarySecretState{Sentinel: public.Sentinel, Shape: public.Shape, Identity: public.Identity, Binding: input.Intent.Binds[name]}
			if err := appendRetiredPublic(result, name, entry); err != nil {
				return err
			}
		}
	}
	for name, entries := range input.Minted {
		if !slices.Contains(member.Secrets, name) {
			return secretIntentFailure()
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := appendSavedMinted(result, name, entry); err != nil {
				return err
			}
		}
	}
	for name, entries := range input.Retired {
		if !slices.Contains(member.Secrets, name) {
			return secretIntentFailure()
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := appendRetiredPublic(result, name, entry); err != nil {
				return err
			}
		}
	}
	return nil
}
