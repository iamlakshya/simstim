package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"reflect"
)

// compilePolicyLayers preserves the explicit order emitted by MergeLayers.
// Policies from older clients omit Order and retain their kind-only grouping.
func compilePolicyLayers(pol api.Policy) ([]compiledLayer, error) {
	if len(pol.Order) == 0 {
		if !reflect.ValueOf(pol.LayerValues).IsZero() || len(pol.Origins) > 0 {
			return nil, badLayerOrder()
		}
		for _, rule := range pol.Rules {
			if rule.Source != "" {
				return nil, badLayerOrder()
			}
		}
		for _, lock := range pol.Locks {
			if !reflect.ValueOf(lock.LayerValues).IsZero() {
				return nil, badLayerOrder()
			}
		}
		return legacyPolicyLayers(pol)
	}
	byRef := map[api.LayerRef][]compiledRule{}
	seen := map[api.LayerRef]bool{}
	kinds := map[api.LayerKind]bool{}
	previous := -1
	for _, ref := range pol.Order {
		r := api.LayerRank(ref.Kind)
		if !api.ValidLayerRef(ref) || seen[ref] || kinds[ref.Kind] && ref.Kind != api.LayerRepo || r < previous {
			return nil, badLayerOrder()
		}
		seen[ref], kinds[ref.Kind], previous = true, true, r
	}
	for _, rule := range pol.Rules {
		if _, err := canonicalRuleMethods(rule); err != nil {
			return nil, err
		}
		ref := api.LayerRef{Kind: rule.Layer, Name: rule.Name, Source: rule.Source}
		if !seen[ref] {
			return nil, badLayerOrder()
		}
		if rule.Door != "" && rule.Door != api.DoorNet {
			continue
		}
		compiled, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		byRef[ref] = append(byRef[ref], compiled)
	}
	var out []compiledLayer
	for i := len(pol.Order) - 1; i >= 0; i-- {
		ref := pol.Order[i]
		if rules := byRef[ref]; len(rules) > 0 {
			out = append(out, compiledLayer{kind: ref.Kind, name: ref.Name, source: ref.Source, rules: rules})
		}
	}
	return out, nil
}

func badLayerOrder() error {
	return &proto.Error{Code: "bad_request", Message: "policy order must name each rule's layer once, from broadest to narrowest", Hint: "Merge the source layers again before sending this policy."}
}

func legacyPolicyLayers(pol api.Policy) ([]compiledLayer, error) {
	byLayer := map[api.LayerKind][]compiledRule{}
	for _, rule := range pol.Rules {
		ref := api.LayerRef{Kind: rule.Layer, Name: rule.Name, Source: rule.Source}
		if !api.ValidLayerRef(ref) || api.LayerRank(rule.Layer) < api.LayerRank(api.LayerAll) || api.LayerRank(rule.Layer) > api.LayerRank(api.LayerLockdown) {
			return nil, badLayerOrder()
		}
		if _, err := canonicalRuleMethods(rule); err != nil {
			return nil, err
		}
		if rule.Door != "" && rule.Door != api.DoorNet {
			continue
		}
		compiled, err := compileRule(rule)
		if err != nil {
			return nil, err
		}
		byLayer[rule.Layer] = append(byLayer[rule.Layer], compiled)
	}
	var out []compiledLayer
	for _, kind := range []api.LayerKind{api.LayerLockdown, api.LayerSession, api.LayerProfile, api.LayerRepo, api.LayerHost, api.LayerAll} {
		if rules := byLayer[kind]; len(rules) > 0 {
			out = append(out, compiledLayer{kind: kind, rules: rules})
		}
	}
	return out, nil
}
