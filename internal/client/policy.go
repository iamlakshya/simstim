package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// PolicyLogEntry is the existing scrubbed network audit record. The data
// belongs to api, so the client does not import or duplicate an egress engine.
type PolicyLogEntry = api.NetAuditEntry

// Policy reads a sandbox's current merged policy through the host helper.
func (c *Conn) Policy(ctx context.Context, id string) (api.Policy, error) {
	var out api.Policy
	if err := c.needPolicy(id); err != nil {
		return out, err
	}
	err := c.Call(ctx, "policy.ls", map[string]string{"id": id}, &out)
	return out, err
}

// CheckPolicyGrant checks a network allowance against the current host tier
// and the session's saved request ceiling. The outgoing operation checks again.
func (c *Conn) CheckPolicyGrant(ctx context.Context, id, target string) error {
	ctx, _ = ensureGrantRequest(ctx)
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	if !ValidHostID(c.HostID()) {
		return grantIdentityError(c.Host)
	}
	settings, err := ReadPolicySettings()
	if err != nil {
		return err
	}
	outbound, err := policyOutbound(target)
	if err != nil {
		return err
	}
	if err := c.needPolicy(id); err != nil {
		return err
	}
	tier, _, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return err
	}
	return allowRequestGrant(ctx, c.Host, c.HostID(), tier, outbound, settings)
}

// AllowPolicy adds a network rule to the session layer. It holds H5's host
// policy ownership through the final gate and the actual initial frame write.
func (c *Conn) AllowPolicy(ctx context.Context, a api.PolicyAddArgs) (api.Rule, error) {
	if a.PublicOnly && a.Door != "" && a.Door != api.DoorNet {
		return api.Rule{}, publicOnlyRuleError()
	}
	if a.Door == api.DoorPort {
		return c.changePortPolicy(ctx, a, true)
	}
	var out api.Rule
	if _, err := ReadPolicySettings(); err != nil {
		return out, err
	}
	var err error
	a, err = c.canonicalPolicyMethods(a)
	if err != nil {
		return out, err
	}
	canonical, _, err := policyTargetGrant(a.Target)
	if err != nil {
		return out, err
	}
	a.Target = canonical
	if err := c.needPolicy(a.ID); err != nil {
		return out, err
	}
	if a.Door != "" && a.Door != api.DoorNet {
		return out, &UserError{Msg: "only the net door is available in this version",
			Hint: "Pass net, or leave the door out.", Code: "bad_door", Kind: KindUsage}
	}
	a.Allow, a.Door = true, api.DoorNet
	ctx, release, err := c.beginGrant(ctx, "policy allow", true)
	if err != nil {
		return out, err
	}
	defer release()
	if err := c.checkPolicyRuleGrant(ctx, a.ID, api.Rule{Allow: true, Door: api.DoorNet, Target: a.Target, PublicOnly: a.PublicOnly}); err != nil {
		return out, err
	}
	err = c.callWithSent(ctx, "policy.add", a, &out, release)
	return out, err
}

// RemovePolicy removes a rule from the session layer. Revocation needs no
// allowance and never raises a session's saved tier ceiling.
func (c *Conn) RemovePolicy(ctx context.Context, a api.PolicyRmArgs) (int, error) {
	if _, err := ReadPolicySettings(); err != nil {
		return 0, err
	}
	if err := c.needPolicy(a.Session); err != nil {
		return 0, err
	}
	var out struct {
		Removed int `json:"removed"`
	}
	err := c.Call(ctx, "policy.rm", a, &out)
	return out.Removed, err
}

// PolicyLog reads scrubbed decisions through the existing policy.log operation.
func (c *Conn) PolicyLog(ctx context.Context, a api.PolicyLogArgs) ([]PolicyLogEntry, error) {
	if err := c.needPolicy(a.ID); err != nil {
		return nil, err
	}
	a.Page, a.Cursor, a.Limit = false, "", 0
	out := []PolicyLogEntry{}
	err := c.Call(ctx, "policy.log", a, &out)
	if out == nil && err == nil {
		out = []PolicyLogEntry{}
	}
	return out, err
}

// PolicyLogPage reads the canonical bounded cursor envelope. The cursor is
// opaque: the client never interprets offsets or de-duplicates audit records.
func (c *Conn) PolicyLogPage(ctx context.Context, a api.PolicyLogArgs) (api.PolicyLogPage, error) {
	out := api.PolicyLogPage{Entries: []api.NetAuditEntry{}}
	if err := c.needPolicy(a.ID); err != nil {
		return out, err
	}
	if !c.HasFeature(api.FeaturePolicyLogCursor) {
		return out, c.grantHelperOld("follow a policy log without losing equal records")
	}
	a.Page, a.Max = true, 0
	if a.Limit == 0 {
		a.Limit = api.PolicyLogDefaultLimit
	}
	if a.Limit < 1 || a.Limit > api.PolicyLogMaxLimit {
		return out, &UserError{Msg: "invalid policy log page limit",
			Hint: "Use a page size from 1 to 256.", Code: "policy_filter", Kind: KindUsage}
	}
	if err := c.Call(ctx, "policy.log", a, &out); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if out.Cursor == "" || len(out.Cursor) > api.PolicyLogMaxCursorBytes || len(out.Entries) > a.Limit || out.More && out.Cursor == a.Cursor {
		return out, &UserError{Msg: "the host returned a policy log page without a valid next cursor",
			Hint: "Update the host helper, then read the policy log again.", Code: "protocol"}
	}
	if out.Entries == nil {
		out.Entries = []api.NetAuditEntry{}
	}
	return out, nil
}

func (c *Conn) needPolicy(id string) error {
	if err := checkSessionID(c.Host.Name, id); err != nil {
		return err
	}
	if !c.HasFeature("policy") {
		return c.grantHelperOld("change a sandbox policy")
	}
	return nil
}
