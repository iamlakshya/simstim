package client

import (
	"github.com/iamlakshya/simstim/internal/api"
)

// CheckHTTPMethodFeature refuses all constrained payloads, including explicit
// empty sets, before any older helper could ignore and broaden them.
func (c *Conn) CheckHTTPMethodFeature(layers []api.Layer) error {
	constrained := false
	for _, layer := range layers {
		for _, rule := range layer.Rules {
			constrained = constrained || rule.Methods != nil
		}
		for _, rule := range layer.Network.Rules {
			constrained = constrained || rule.Methods != nil
		}
		if layer.Lock != nil {
			for _, rule := range layer.Lock.Network.Rules {
				constrained = constrained || rule.Methods != nil
			}
		}
	}
	if constrained && !c.HasFeature(api.FeatureHTTPMethods) {
		return c.grantHelperOld("preserve exact HTTP method constraints")
	}
	return nil
}

func (c *Conn) canonicalPolicyMethods(a api.PolicyAddArgs) (api.PolicyAddArgs, error) {
	if a.Methods != nil && !c.HasFeature(api.FeatureHTTPMethods) {
		return a, c.grantHelperOld("preserve exact HTTP method constraints")
	}
	methods, err := api.CanonicalHTTPMethods(a.Methods)
	if err != nil {
		return a, &UserError{Msg: err.Error(), Hint: "Use exact HTTP tokens such as GET or POST.", Code: "bad_method", Kind: KindUsage}
	}
	a.Methods = methods
	return a, nil
}
