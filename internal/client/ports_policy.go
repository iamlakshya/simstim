package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// CheckPortPolicyGrant checks the current local creation and tier before a
// session port allowance, including a dry run. It opens no laptop service.
func (c *Conn) CheckPortPolicyGrant(ctx context.Context, session string) error {
	if !c.HasFeature(api.FeatureSandboxPortsV1) {
		return portsUpgrade()
	}
	ctx, _ = ensureGrantRequest(ctx)
	ctx, release, err := c.beginGrant(ctx, "port policy", false)
	if err != nil {
		return err
	}
	defer release()
	return c.checkPortPolicyGrant(ctx, session)
}

func (c *Conn) checkPortPolicyGrant(ctx context.Context, session string) error {
	tier, creation, err := EffectiveSessionTier(ctx, c.Host, session, c.HostID())
	if err != nil {
		return err
	}
	if creation == nil || creation.FutureLineage || creation.CreationWall == "" || creation.CreationWall == "none" {
		return &UserError{Msg: "the port allowance has no supported local sandbox record", Hint: "Resume the sandbox through the current client first.", Code: "grant_state", Kind: KindRefused}
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		return err
	}
	return Allow(c.Host, tier, Outbound{Kind: OutboundForward}, settings)
}

func (c *Conn) changePortPolicy(ctx context.Context, args api.PolicyAddArgs, allow bool) (api.Rule, error) {
	var result api.Rule
	if err := c.needPolicy(args.ID); err != nil {
		return result, err
	}
	if !c.HasFeature(api.FeatureSandboxPortsV1) {
		return result, portsUpgrade()
	}
	if _, err := api.ParsePortTarget(args.Target); err != nil || args.Methods != nil || len(args.Tools) != 0 || args.Mode != "" || args.Once || !allow && !args.Until.IsZero() {
		return result, &UserError{Msg: "the port rule is not an exact supported session rule", Hint: "Name one decimal port, without methods, mode or --once.", Code: "bad_port", Kind: KindUsage}
	}
	args.Door, args.Allow = api.DoorPort, allow
	if !allow {
		err := c.Call(ctx, "policy.add", args, &result)
		return result, err
	}
	ctx, _ = ensureGrantRequest(ctx)
	ctx, release, err := c.beginGrant(ctx, "port policy allow", false)
	if err != nil {
		return result, err
	}
	defer release()
	if err := c.checkPortPolicyGrant(ctx, args.ID); err != nil {
		return result, err
	}
	err = c.callWithSent(ctx, "policy.add", args, &result, release)
	return result, err
}
