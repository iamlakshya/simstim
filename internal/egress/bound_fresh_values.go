package egress

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidateFreshValuesAgainstPublicState checks supplied fresh values against
// every primary, minted and retired stand-in in a complete saved cohort. The
// supplied names can be a subset or new child names; unrelated cohort values
// are unnecessary. This is pure data validation. The caller retains original
// name, Source and final acknowledgement authority checks.
func ValidateFreshValuesAgainstPublicState(ctx context.Context, state *api.SessionSecretState, values map[string]api.Secret) error {
	if err := sentinelContext(ctx, "registry"); err != nil {
		return err
	}
	owned := api.CloneSessionSecretState(state)
	if err := ValidateSavedPublicSessionState(owned); err != nil {
		return err
	}
	actuals := make(map[api.Secret]bool, len(values))
	for name, value := range values {
		if err := sentinelContext(ctx, "registry"); err != nil {
			return err
		}
		if err := checkRegistryValue(name, value); err != nil {
			return err
		}
		actuals[value] = true
	}
	if err := checkAcknowledgedActualCollisions(ctx, owned, actuals); err != nil {
		return err
	}
	return sentinelContext(ctx, "registry")
}
