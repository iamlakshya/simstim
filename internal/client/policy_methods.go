package client

import (
	"github.com/iamlakshya/simstim/internal/api"
)

// CheckHTTPMethodFeature refuses all constrained payloads, including explicit
// empty sets, before any older helper could ignore and broaden them.
func (c *Conn) CheckHTTPMethodFeature(layers []api.Layer) error {
	if err := c.checkPublicOnlyFeature(layers); err != nil {
		return err
	}
	constrained, modelTools := false, false
	for _, layer := range layers {
		for _, rule := range layer.Rules {
			constrained = constrained || rule.Methods != nil
			modelTools = modelTools || len(rule.Tools) != 0
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
	if modelTools && !c.HasFeature(api.FeatureModelTools) {
		return c.grantHelperOld("preserve scoped model capabilities")
	}
	if constrained && !c.HasFeature(api.FeatureHTTPMethods) {
		return c.grantHelperOld("preserve exact HTTP method constraints")
	}
	return nil
}

func (c *Conn) canonicalPolicyMethods(a api.PolicyAddArgs) (api.PolicyAddArgs, error) {
	if a.PublicOnly {
		if a.Door != "" && a.Door != api.DoorNet {
			return a, publicOnlyRuleError()
		}
		if !c.HasFeature(api.FeatureNetworkPublicOnly) {
			return a, c.grantHelperOld("preserve public-only network rules")
		}
	}
	if a.Methods != nil && !c.HasFeature(api.FeatureHTTPMethods) {
		return a, c.grantHelperOld("preserve exact HTTP method constraints")
	}
	if len(a.Tools) != 0 && !c.HasFeature(api.FeatureModelTools) {
		return a, c.grantHelperOld("preserve scoped model capabilities")
	}
	tools, err := api.CanonicalModelTools(a.Tools)
	if err != nil {
		return a, &UserError{Msg: err.Error(), Hint: "Use web_fetch, web_search, code_execution, mcp, url_content or files.", Code: "bad_tools", Kind: KindUsage}
	}
	a.Tools = tools
	methods, err := api.CanonicalHTTPMethods(a.Methods)
	if err != nil {
		return a, &UserError{Msg: err.Error(), Hint: "Use exact HTTP tokens such as GET or POST.", Code: "bad_method", Kind: KindUsage}
	}
	a.Methods = methods
	return a, nil
}
