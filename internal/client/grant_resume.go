package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// CheckResumeGrants checks helper capability and this laptop's current
// creation policy before a frontend fetches any resume values. It never
// publishes a request. Resume checks again under its publication lock.
func (c *Conn) CheckResumeGrants(ctx context.Context, id string, names []string) error {
	ctx, _ = ensureGrantRequest(ctx)
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return err
	}
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return err
	}
	if err := c.checkNoPendingInputResume(ctx, id); err != nil {
		return err
	}
	if err := checkUncheckedResumeSelection(ctx, c, nil); err != nil {
		return err
	}
	if !c.HasFeature("resume") {
		return c.tooOldToResume()
	}
	if err := c.requireSecretFeature(len(names) != 0); err != nil {
		return err
	}
	group, err := c.needsResumeMembers(ctx, id)
	if err != nil {
		return err
	}
	if group {
		_, _, err := c.prepareResumeMembers(ctx, id, names)
		return err
	}
	_, err = c.checkResumeGrantNames(ctx, id, names)
	return err
}

func (c *Conn) checkResumeGrants(ctx context.Context, a ResumeArgs) (Tier, error) {
	if err := checkUncheckedResumeSelection(ctx, c, nil); err != nil {
		return TierUnknown, err
	}
	group, err := c.needsResumeMembers(ctx, a.ID)
	if err != nil {
		return TierUnknown, err
	}
	if group || a.Snapshot != nil || a.Members != nil {
		return c.checkResumeBundle(ctx, a)
	}
	return c.checkResumeGrantNames(ctx, a.ID, secretMapNames(a.Secrets))
}

func (c *Conn) checkResumeGrantNames(ctx context.Context, id string, names []string) (Tier, error) {
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return TierUnknown, err
	}
	tier, p, err := EffectiveSessionTier(ctx, c.Host, id, c.HostID())
	if err != nil {
		return TierUnknown, err
	}
	if err := checkUncheckedResumeSelection(ctx, c, p); err != nil {
		return TierUnknown, err
	}
	if err := bindLegacyResumeIntent(ctx, c.Host, c.HostID(), id, names, p); err != nil {
		return TierUnknown, err
	}
	settings := LoadSettings()
	if p != nil && p.SSHAgent {
		if err := allowRequestGrant(ctx, c.Host, c.HostID(), tier, Outbound{Kind: OutboundSSHAgent}, settings); err != nil {
			return tier, err
		}
		if !c.HasFeature(agentbridge.Feature) {
			return tier, c.grantHelperOld("share an agent with one session")
		}
	}
	if err := allowResumeSecretNames(ctx, c.Host, c.HostID(), tier, settings, names, p); err != nil {
		return tier, err
	}
	return tier, nil
}
