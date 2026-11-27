package host

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// PrepareSecretDelivery validates an actual publication before lifecycle IO.
// allowed is nil only for a new request. A resume/member uses its original
// recorded names. These host checks never authorise a laptop fetch or tier.
func PrepareSecretDelivery(intent *api.SecretDeliveryIntent, values map[string]api.Secret, walled bool, allowed []string) (*api.SecretDeliveryIntent, []string, error) {
	actual, err := checkSecrets(values)
	if err != nil {
		return nil, nil, err
	}
	if intent == nil {
		return nil, actual, nil
	}
	refuse := func() (*api.SecretDeliveryIntent, []string, error) {
		return nil, nil, UserError("bad_secret_delivery", "secret delivery metadata is unsupported or inconsistent", "Use matching helpers and the original locally approved secret names and modes.")
	}
	if intent.Version != api.SecretDeliveryIntentV1 {
		return refuse()
	}
	var allowedSet map[string]struct{}
	if allowed != nil {
		allowedSet = make(map[string]struct{}, len(allowed))
		for _, name := range allowed {
			allowedSet[name] = struct{}{}
		}
	}
	owned := api.CloneSecretDeliveryIntent(*intent)
	for name, use := range owned.TrustedUses {
		if !walled || egress.ValidateTrustedSecretUseV1(name, use) != nil {
			return refuse()
		}
		binding, err := secretbind.Canonical(use.Bind)
		if err != nil {
			return refuse()
		}
		use.Bind = binding
		owned.TrustedUses[name] = use
	}
	names := slices.Sorted(maps.Keys(owned.Modes))
	if allowedSet != nil && len(names) != len(allowedSet) {
		return refuse()
	}
	for _, name := range names {
		if name == "SSH_AUTH_SOCK" {
			return refuse()
		}
		// Reuse the canonical existing host environment-name barrier.
		if _, err := checkSecrets(map[string]api.Secret{name: ""}); err != nil {
			return nil, nil, err
		}
		if allowedSet != nil {
			if _, ok := allowedSet[name]; !ok {
				return refuse()
			}
		}
		mode := owned.Modes[name]
		binding, hasBinding := owned.Binds[name]
		if !mode.Valid() || mode == api.SecretModeExposed && hasBinding {
			return refuse()
		}
		if mode != api.SecretModeBound {
			continue
		}
		if !walled || !hasBinding {
			return refuse()
		}
		binding, err = secretbind.Canonical(binding)
		if err != nil {
			return refuse()
		}
		owned.Binds[name] = binding
	}
	for name := range owned.Binds {
		if owned.Modes[name] != api.SecretModeBound {
			return refuse()
		}
	}
	for name := range values {
		if _, present := owned.Modes[name]; !present {
			return refuse()
		}
	}
	return &owned, names, nil
}
