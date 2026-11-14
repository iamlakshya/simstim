package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidateSavedPublicSessionState validates a complete saved public envelope
// before a value frame. It needs no CA or entropy and supplies no permission
// to fetch a name. Actual request/result sizes use the full frame validators.
func ValidateSavedPublicSessionState(state *api.SessionSecretState) error {
	if !savedPublicStateEnvelope(state) {
		return invalidPublicSessionState()
	}
	return validatePublicSessionEntries(api.CloneSessionSecretState(state))
}

func savedPublicStateEnvelope(state *api.SessionSecretState) bool {
	return state != nil && state.Supported() && validRegistryNonce(state.Generation) && len(state.Intent.Modes) == len(state.Primary)
}

// CloneLocalSessionSecretState validates and owns an already selected public
// receipt state using the same canonical grammar. Every key, including retired
// tripwires, must lie within the caller's original LOCAL name domain. This is
// data validation only: it proves neither receipt provenance nor completeness,
// fetch permission, a matching operation or a current host/source identity.
// It is the selected full-state path, not an unselected FD metadata validator.
func CloneLocalSessionSecretState(state *api.SessionSecretState, localNames map[string]bool) (*api.SessionSecretState, error) {
	owned := api.CloneSessionSecretState(state)
	if !savedPublicStateEnvelope(owned) {
		return nil, invalidPublicSessionState()
	}
	if !localPublicStateNames(owned.Intent.Modes, localNames) || !localPublicStateNames(owned.Intent.Binds, localNames) ||
		!localPublicStateNames(owned.Primary, localNames) || !localPublicStateNames(owned.Minted, localNames) || !localPublicStateNames(owned.Retired, localNames) {
		return nil, memberError("secret_state", "public receipt metadata lies outside its original local name domain",
			"Keep the work in quarantine and use the original acknowledged local publication receipt.")
	}
	if err := validatePublicSessionEntries(owned); err != nil {
		return nil, err
	}
	return owned, nil
}

func localPublicStateNames[T any](values map[string]T, localNames map[string]bool) bool {
	for name := range values {
		if !localNames[name] {
			return false
		}
	}
	return true
}

// ValidatePublicSecretValue validates one returned armed primary against the
// fresh approved value. The producer still checks exact result domains and
// every public stand-in against ALL fresh actual values, including exposed
// entries. This helper fetches, authorises, persists and publishes nothing.
func ValidatePublicSecretValue(ctx context.Context, name string, public api.PublicSecretState, binding api.SecretBind, value api.Secret) error {
	if err := checkRegistryValue(name, value); err != nil {
		return err
	}
	if public.Mode != api.SecretModeBound || !public.Armed || !validRegistryNonce(public.Identity) || public.Sentinel == value || !validBoundCredential(value) {
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
	if err := checkSentinelValue(ctx, name, value, bound.shape); err != nil {
		return err
	}
	return checkMintedHTTPValue(name, canonical, value)
}
