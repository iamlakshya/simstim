package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/nettarget"
)

// checkLayerGrants uses H5 for every allowance at the actual outbound boundary.
// File layers are local configuration; they acquire no independent authority.
func (c *Conn) checkLayerGrants(ctx context.Context, tier Tier, layers []api.Layer) error {
	return c.allowLayerGrants(ctx, tier, layers, true)
}

func (c *Conn) allowLayerGrants(ctx context.Context, tier Tier, layers []api.Layer, record bool) error {
	settings, err := ReadPolicySettings()
	if err != nil {
		return err
	}
	for _, layer := range layers {
		for _, rule := range layer.Rules {
			if !rule.Allow {
				continue
			}
			if rule.Door != "" && rule.Door != api.DoorNet {
				continue
			}
			outbound, err := policyOutbound(rule.Target)
			if err != nil {
				return err
			}
			if err := allowRequestGrant(ctx, c.Host, c.HostID(), tier, outbound, settings); err != nil {
				return err
			}
			if record {
				if err := recordRequestGrant(ctx, c.Host, c.HostID(), tier, outbound); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// CheckNewPolicyGrants checks every layered network allowance before a front
// end fetches values. It neither records an exposure nor sends a mutation.
func (c *Conn) CheckNewPolicyGrants(ctx context.Context, layers []api.Layer) error {
	names, err := PolicySecretNames(layers)
	if err != nil {
		return err
	}
	tier, _, err := c.newGrantTier(ctx, names, false)
	if err != nil {
		return err
	}
	return c.allowLayerGrants(ctx, tier, layers, false)
}

func policyOutbound(target string) (Outbound, error) {
	canonical, private, err := policyTargetGrant(target)
	if err != nil {
		return Outbound{}, err
	}
	kind := OutboundNet
	if private {
		kind = OutboundPrivateNet
	}
	return Outbound{Kind: kind, Name: canonical}, nil
}

func policyTargetGrant(target string) (string, bool, error) {
	canonical, private, err := nettarget.Classify(target)
	if err != nil {
		return "", false, &UserError{Msg: "invalid network target: " + CleanLine(err.Error()),
			Hint: "Pass a host, an IP address or a CIDR, with an optional port or path.", Code: "bad_target", Kind: KindUsage}
	}
	return canonical, private, nil
}
