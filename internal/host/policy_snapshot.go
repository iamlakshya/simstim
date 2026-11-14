package host

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// PolicySnapshot supplies read-only canonical source layers for a next-start
// preview. The caller supplies an owned api.CloneLayers snapshot, taken under
// its state lock. Sources is attached only to the response, never stored.
func PolicySnapshot(layers []api.Layer) (api.Policy, error) {
	policy, clean, err := egress.MergeLayers(layers)
	if err != nil {
		return api.Policy{}, err
	}
	policy.Sources = clean
	return policy, nil
}
