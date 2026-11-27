package egress

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/secretbind"
)

// Main consumes this immutable GLOBAL generation projection under the same
// gate. Every request-eligible variant has its own identity and armed state.
// Historical primary values remain response/audit only.
func registryRequestEntries(state *memberState) ([]requestBoundSecret, error) {
	var entries []requestBoundSecret
	appendEntry := func(name, identity string, binding api.SecretBind, value, sentinel api.Secret, armed bool) error {
		kind, user, err := secretbind.ParseFormat(binding.Format)
		if err != nil {
			return registryIntentError()
		}
		for _, candidate := range []api.Secret{sentinel} {
			if field, valid := requestCredentialValue(kind, user, candidate); !valid || len(field)+len(binding.Header)+4 > maxHTTPHeaderBytes {
				return memberError("secret_format", "secret "+name+" has an unrepresentable credential stand-in", "Stop and resume with a supported credential format.")
			}
		}
		if armed {
			if field, valid := requestCredentialValue(kind, user, value); !valid || len(field)+len(binding.Header)+4 > maxHTTPHeaderBytes {
				return memberError("secret_format", "secret "+name+" has an unrepresentable credential value", "Use a supported bound credential format.")
			}
		}
		entries = append(entries, requestBoundSecret{Name: name, Identity: identity, Binding: binding, Value: value, Sentinel: sentinel, Armed: armed})
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(state.secrets)) {
		secret := state.secrets[name]
		if secret.bound == nil {
			continue
		}
		if err := appendEntry(name, secret.identity, secret.bound.binding, secret.value, secret.bound.sentinel, secret.present); err != nil {
			return nil, err
		}
		for _, mint := range secret.minted {
			binding := secret.bound.binding
			binding.Hosts = []string{mint.authority}
			if err := appendEntry(name, mint.identity, binding, mint.value, mint.sentinel, mint.ready && !mint.retired); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}
