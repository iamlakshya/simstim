package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

func publicOnlyRuleError() error {
	return &UserError{Msg: "public_only is a restriction for an allowed network rule", Hint: "Use it only with an allow on the net door.", Code: "bad_rule", Kind: KindUsage}
}
func (c *Conn) checkPublicOnlyFeature(layers []api.Layer) error {
	if api.LayersUsePublicOnly(layers) && !c.HasFeature(api.FeatureNetworkPublicOnly) {
		return c.grantHelperOld("preserve public-only network rules")
	}
	return nil
}
func policyOutboundForRule(rule api.Rule) (Outbound, error) {
	canonical, private, err := policyTargetGrant(rule.Target)
	if err != nil {
		return Outbound{}, err
	}
	if rule.PublicOnly {
		if !rule.Allow || rule.Door != "" && rule.Door != api.DoorNet {
			return Outbound{}, publicOnlyRuleError()
		}
		// This exact canonical target is narrowed by the explicit persisted bit.
		// It cannot supply a numeric/private-address exception at runtime.
		return Outbound{Kind: OutboundNet, Name: canonical}, nil
	}
	kind := OutboundNet
	if private {
		kind = OutboundPrivateNet
	}
	return Outbound{Kind: kind, Name: canonical}, nil
}
func (c *Conn) checkPolicyRuleGrant(ctx context.Context, id string, rule api.Rule) error {
	if !rule.PublicOnly {
		return c.CheckPolicyGrant(ctx, id, rule.Target)
	}
	if err := c.checkPublicOnlyFeature([]api.Layer{{Rules: []api.Rule{rule}}}); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	if !ValidHostID(c.HostID()) {
		return grantIdentityError(c.Host)
	}
	if err := c.needPolicy(id); err != nil {
		return err
	}
	settings, err := requestPolicySettings(ctx)
	if err != nil {
		return err
	}
	tier, _, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return err
	}
	outbound, err := policyOutboundForRule(rule)
	if err != nil {
		return err
	}
	return allowRequestGrant(ctx, c.Host, c.HostID(), tier, outbound, settings)
}
