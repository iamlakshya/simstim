package egress

import "github.com/iamlakshya/simstim/internal/api"

// ProjectLocalSessionSecretStateV1 validates complete public metadata before
// reducing guest maps to an already authorised local domain. Trusted uses
// retain their separate namespace. This supplies no ACK, Source, fetch,
// publication or native authority; the actual operation owns those checks.
func ProjectLocalSessionSecretStateV1(state *api.SessionSecretState, localNames map[string]bool) (*api.SessionSecretState, error) {
	if err := ValidateSavedPublicSessionState(state); err != nil {
		return nil, err
	}
	owned := api.CloneSessionSecretState(state)
	retainLocalGuestMetadataV1(owned.Intent.Modes, localNames)
	retainLocalGuestMetadataV1(owned.Intent.Binds, localNames)
	retainLocalGuestMetadataV1(owned.Primary, localNames)
	retainLocalGuestMetadataV1(owned.Minted, localNames)
	retainLocalGuestMetadataV1(owned.Retired, localNames)
	return CloneLocalSessionSecretState(owned, localNames)
}

func retainLocalGuestMetadataV1[T any](values map[string]T, localNames map[string]bool) {
	for name := range values {
		if !localNames[name] {
			delete(values, name)
		}
	}
}
