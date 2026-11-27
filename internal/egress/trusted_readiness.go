package egress

import (
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

const CtlTrustedReadiness = "trusted.readiness"

// This names-only read belongs to the already owned Egress control actor.
// Expected original tuples are compared before any armed state is returned.
func (s *Server) trustedSecretReadiness(raw []byte) (api.TrustedSecretReadinessV1, error) {
	var query api.TrustedSecretReadinessQueryV1
	if !api.ExactJSONFields(raw, query) {
		return api.TrustedSecretReadinessV1{}, registryIntentError()
	}
	if err := decodeControlArgs(raw, &query); err != nil {
		return api.TrustedSecretReadinessV1{}, err
	}
	if query.Version != 1 || !validRegistryNonce(query.Generation) || len(query.Uses) == 0 {
		return api.TrustedSecretReadinessV1{}, registryIntentError()
	}
	ctx, cancel := s.controlMutationContext()
	defer cancel()
	if err := s.policyMu.LockContext(ctx); err != nil {
		return api.TrustedSecretReadinessV1{}, err
	}
	defer s.policyMu.Unlock()
	state := s.currentMembers()
	s.flows.mu.Lock()
	closing := s.flows.closing
	s.flows.mu.Unlock()
	if closing || state == nil || state.version != MemberRegistryV2 || state.generation != query.Generation {
		return api.TrustedSecretReadinessV1{}, memberError("secret_generation", "the owned trusted role generation changed or ended", "Refresh the original protected cohort before considering a re-arm.")
	}
	result := api.TrustedSecretReadinessV1{Version: 1, Generation: state.generation, Unarmed: make([]string, 0)}
	for _, name := range slices.Sorted(maps.Keys(query.Uses)) {
		expected, err := canonicalTrustedUse(name, query.Uses[name])
		if err != nil {
			return api.TrustedSecretReadinessV1{}, err
		}
		if !trustedUseMatchesPolicy(name, expected, state.metadata.Policy) {
			return api.TrustedSecretReadinessV1{}, registryIntentError()
		}
		secret, exists := state.secrets[name]
		// A role which is absent or different is positively known to be
		// unarmed for this exact desired tuple. This negative observation
		// neither registers that tuple nor authorises a value fetch.
		exact := exists && secret.trusted != nil && sameTrustedUseMetadata(map[string]api.TrustedSecretUseV1{name: expected}, map[string]api.TrustedSecretUseV1{name: *secret.trusted})
		if !exact || !secret.present || !secret.trustedArmed {
			result.Unarmed = append(result.Unarmed, name)
		}
	}
	if !result.ValidFor(query) {
		return api.TrustedSecretReadinessV1{}, registryIntentError()
	}
	return result, nil
}
