package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// DenyPolicy adds a session deny through the existing canonical policy.add
// operation. Revocation needs no allowance or host-tier confirmation.
func (c *Conn) DenyPolicy(ctx context.Context, a api.PolicyAddArgs) (api.Rule, error) {
	var out api.Rule
	if _, err := ReadPolicySettings(); err != nil {
		return out, err
	}
	var err error
	a, err = c.canonicalPolicyMethods(a)
	if err != nil {
		return out, err
	}
	if err := c.needPolicy(a.ID); err != nil {
		return out, err
	}
	if a.Door != "" && a.Door != api.DoorNet {
		return out, &UserError{Msg: "only the net door is available in this version", Hint: "Pass net, or leave the door out.", Code: "bad_door", Kind: KindUsage}
	}
	canonical, _, err := policyTargetGrant(a.Target)
	if err != nil {
		return out, err
	}
	if a.Once || !a.Until.IsZero() {
		return out, &UserError{Msg: "only an allow may be temporary", Hint: "Use policy deny without --once or --for.", Code: "layer_key", Kind: KindUsage}
	}
	a.Allow, a.Door, a.Target = false, api.DoorNet, canonical
	if err := c.Call(ctx, "policy.add", a, &out); err != nil {
		return out, err
	}
	return out, nil
}
