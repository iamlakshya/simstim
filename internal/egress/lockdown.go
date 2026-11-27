package egress

import (
	"cmp"
	"github.com/iamlakshya/simstim/internal/api"
	"slices"
)

func hasLockdownLayer(policy api.Policy) bool {
	for _, layer := range policy.Order {
		if layer.Kind == api.LayerLockdown {
			return true
		}
	}
	for _, rule := range policy.Rules {
		if rule.Layer == api.LayerLockdown {
			return true
		}
	}
	return false
}

// The native empty lock layer is an emergency projection, independent of
// ordinary scalar locks. Its complete canonical source vector stays empty;
// the actual policy/read projection records the two effective overrides.
func applyLockdownProjection(policy *api.Policy) {
	if policy == nil || !hasLockdownLayer(*policy) {
		return
	}
	policy.Preset, policy.Unknown = api.PresetLocked, api.UnknownDeny
	policy.Sandbox.Preset, policy.Network.Unknown = new("locked"), new("deny")
	policy.Origins = slices.DeleteFunc(policy.Origins, func(origin api.ValueOrigin) bool {
		return origin.Key == "sandbox.preset" || origin.Key == "network.unknown"
	})
	policy.Origins = append(policy.Origins, api.ValueOrigin{Key: "sandbox.preset", Layer: api.LayerLockdown}, api.ValueOrigin{Key: "network.unknown", Layer: api.LayerLockdown})
	slices.SortFunc(policy.Origins, func(a, b api.ValueOrigin) int {
		if order := cmp.Compare(a.Key, b.Key); order != 0 {
			return order
		}
		return cmp.Compare(a.Item, b.Item)
	})
}
