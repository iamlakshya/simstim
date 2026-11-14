package egress

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// DropOnceRules is the canonical next-start cleanup. It keeps surviving
// legacy leases, removes once/expired rules and never changes its input.
func DropOnceRules(layers []api.Layer, now time.Time) (api.Policy, []api.Layer, error) {
	next := api.CloneLayers(layers)
	for i, layer := range next {
		var kept []api.Rule
		for _, rule := range layer.Rules {
			if rule.Once || !rule.Until.IsZero() && !now.Before(rule.Until) {
				continue
			}
			kept = append(kept, rule)
		}
		next[i].Rules = kept
	}
	return MergeLayers(next)
}
