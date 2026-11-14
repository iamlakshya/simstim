package egress

import (
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// CanonicalNetworkRule returns the one canonical structured file tuple.
func CanonicalNetworkRule(raw api.NetworkRule) (api.NetworkRule, error) {
	target, err := ValidateRuleTarget(raw.Target)
	if err != nil {
		return raw, err
	}
	raw.Target = target
	methods, err := api.CanonicalHTTPMethods(raw.Methods)
	if err != nil {
		return raw, badLayerValue("network.rules.methods", err.Error())
	}
	raw.Methods = methods
	return raw, nil
}

func convertNetworkRules(layer *api.Layer) error {
	var removals []api.NetworkRule
	for _, raw := range layer.Network.Rules {
		raw, err := CanonicalNetworkRule(raw)
		if err != nil {
			return err
		}
		if raw.Remove {
			if layer.Source == api.SourceRepositoryFile {
				return badLayerValue("network.rules.remove", "is not allowed in a repository file")
			}
			removals = append(removals, raw)
			continue
		}
		layer.Rules = append(layer.Rules, api.Rule{Door: api.DoorNet, Target: raw.Target, Allow: raw.Allow, Methods: raw.Methods})
	}
	// Retain only removal directives so each read reapplies them to fresh
	// broader layers, without resurrecting a converted rule after policy rm.
	layer.Network.Rules = removals
	return nil
}

func applyNetworkRemovals(rules []api.Rule, layer api.Layer, locks []api.Lock) ([]api.Rule, error) {
	for _, remove := range layer.Network.Rules {
		if !remove.Remove {
			continue
		}
		for _, lock := range locks {
			for _, deny := range lock.Deny {
				if !remove.Allow && remove.Target == deny {
					return nil, layerError(layer, "network.rules.remove", lock)
				}
			}
			for _, deny := range lock.Network.Rules {
				if !remove.Allow && remove.Target == deny.Target && api.HTTPMethodSetsIntersect(remove.Methods, deny.Methods) {
					return nil, layerError(layer, "network.rules.remove", lock)
				}
			}
		}
		rules = slices.DeleteFunc(rules, func(rule api.Rule) bool {
			return (rule.Door == "" || rule.Door == api.DoorNet) && rule.Target == remove.Target && rule.Allow == remove.Allow && api.SameHTTPMethods(rule.Methods, remove.Methods)
		})
	}
	return rules, nil
}
