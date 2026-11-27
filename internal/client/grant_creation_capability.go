package client

import "context"

// checkCreationCapability does no confirmation. A legacy helper cannot
// publish a client-chosen ID, so it cannot durably bind creation authority
// before an own-tier value or a wall-dependent request leaves the client.
func (c *Conn) checkCreationCapability(ctx context.Context, tier Tier, settings Settings, names []string) error {
	if c.HasFeature("session.id") {
		return nil
	}
	intent := requestWallIntent(ctx)
	needsIdentity := intent != nil && intent.level != "none"
	for _, name := range names {
		// Confirmed distinguishes eligibility from a pending question only.
		// This pure call neither records an answer nor authorises a send.
		if err := Allow(c.Host, tier, Outbound{Kind: OutboundSecret, Name: name, Confirmed: true}, settings); err != nil {
			return err
		}
		minimum, err := minimumSecretTier(settings, name)
		if err != nil {
			return err
		}
		needsIdentity = needsIdentity || minimum == TierOwn
	}
	if needsIdentity {
		return c.grantHelperOld("record the creation wall before receiving an own-tier secret or a wall-dependent request")
	}
	return nil
}
