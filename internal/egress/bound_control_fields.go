package egress

import "github.com/iamlakshya/simstim/internal/api"

// The sole DTO-tag walker supplies exact keys and duplicates. The existing
// decoder still owns ordinary field types and all semantic validation.
func decodeBoundControlArgs(raw []byte, output any) error {
	if !api.ExactJSONFields(raw, output) {
		return registryControlError()
	}
	return decodeControlArgs(raw, output)
}
