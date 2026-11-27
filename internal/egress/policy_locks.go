package egress

import "github.com/iamlakshya/simstim/internal/api"

func compilePolicyLocks(locks []api.Lock) ([]compiledRule, error) {
	var result []compiledRule
	for _, lock := range locks {
		rules := append([]api.NetworkRule{}, lock.Network.Rules...)
		for _, target := range lock.Deny {
			rules = append(rules, api.NetworkRule{Target: target})
		}
		for _, raw := range rules {
			if raw.Allow || raw.Remove {
				return nil, badLayerValue("lock.network.rules", "holds denies only, without removals")
			}
			canonical, err := CanonicalNetworkRule(raw)
			if err != nil {
				return nil, err
			}
			compiled, err := compileRule(api.Rule{Layer: lock.Layer, Name: lock.Name, Door: api.DoorNet,
				Target: canonical.Target, Methods: canonical.Methods, PublicOnly: canonical.PublicOnly})
			if err != nil {
				return nil, err
			}
			compiled.id = "lock:" + compiled.id
			result = append(result, compiled)
		}
	}
	return result, nil
}
