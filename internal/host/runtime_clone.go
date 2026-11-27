package host

import (
	"maps"
	"slices"
)

// cloneRuntime owns mutable runtime retention and opaque bytes without a JSON
// round trip. All current scalar DTO fields remain copied by value.
func cloneRuntime(original Runtime) Runtime {
	result := original
	result.Artefacts = slices.Clone(original.Artefacts)
	result.extra = maps.Clone(original.extra)
	for name, raw := range result.extra {
		result.extra[name] = slices.Clone(raw)
	}
	return result
}
