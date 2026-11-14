package egress

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// ValidatePublicMintedSecretState checks public persistence metadata against
// its already approved owner binding. It fetches no value, changes no state
// and grants no network access. The caller must also require a bound owner.
func ValidatePublicMintedSecretState(ctx context.Context, name string, public api.PublicMintedSecretState, binding api.SecretBind) error {
	_, err := validatedMintedShape(ctx, name, public, binding)
	return err
}

func validatedMintedShape(ctx context.Context, name string, public api.PublicMintedSecretState, binding api.SecretBind) (sentinelShape, error) {
	if err := checkRegistryValue(name, ""); err != nil {
		return sentinelShape{}, err
	}
	canonical, err := canonicalRegistryBinding(name, binding)
	if err != nil {
		return sentinelShape{}, err
	}
	if !validRegistryNonce(public.Identity) || public.Rule == "" || public.Shape == nil || public.Shape.Rule != public.Rule || !slices.Contains(canonical.Hosts, public.Authority) {
		return sentinelShape{}, registryIntentError()
	}
	shape, err := restoreSecretShape(ctx, name, api.PublicSecretState{Mode: api.SecretModeBound, Sentinel: public.Sentinel, Shape: public.Shape}, "", bindingPunctuationPolicy(canonical))
	if err != nil {
		return sentinelShape{}, err
	}
	if err := checkMintedHTTPValue(name, canonical, public.Sentinel); err != nil {
		return sentinelShape{}, err
	}
	return shape, nil
}

func validRegistryNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, b := range []byte(value) {
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return false
		}
	}
	return true
}
