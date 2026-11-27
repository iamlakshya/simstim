package host

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// ApplyPolicyLockdownLayersV1 adds or removes the restriction alone. Original
// underlying rules, locks, sources and grants remain the protected input.
// Its data result creates no native start, arm or new positive allowance.
func ApplyPolicyLockdownLayersV1(layers []api.Layer, on bool) (api.Policy, []api.Layer, error) {
	if _, _, err := egress.MergeLayers(api.CloneLayers(layers)); err != nil {
		return api.Policy{}, nil, err
	}
	next := api.CloneLayers(layers)
	next = slices.DeleteFunc(next, func(layer api.Layer) bool { return layer.Kind == api.LayerLockdown })
	if on {
		next = append(next, api.Layer{Kind: api.LayerLockdown})
	}
	return egress.MergeLayers(next)
}
