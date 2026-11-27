package client

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// This check deliberately does not call checkLocal: it runs beneath the
// config owner's mutex at every native source/publication snapshot.
func (c *networkHoldConfigV1) checkCurrentActionLocked(ctx context.Context) error {
	o := c.owner
	o.mu.Lock()
	conn, original, args, descriptor := o.conn, o.creation, o.args, o.descriptor
	closed := o.closed
	o.mu.Unlock()
	if closed || conn == nil {
		return networkHoldConfigErrorV1()
	}
	if descriptor != nil && !time.Now().Before(descriptor.Expires) {
		return networkHoldSourceError()
	}
	tier, current, err := EffectiveSessionTier(ctx, conn.Host, args.Session, conn.HostID())
	if err != nil {
		return err
	}
	if !tier.Valid() || !sameSandboxCreation(current, original) || current != nil &&
		(current.FutureLineage || current.FutureDelivery || current.futureDeliveryReceipt || current.futureInputOperation) {
		return networkHoldSourceError()
	}
	o.mu.Lock()
	o.tier = min(o.tier, tier, requestCeiling(ctx, tier))
	if current != nil {
		o.tier = min(o.tier, current.Ceiling)
	}
	if o.tier < TierOwn {
		o.maximum = api.NetworkHoldPublicOnly
	}
	// Encoded bytes may not retain an older, wider authorisation after
	// waiting on a file lock or writing a temporary file.
	changed := c.authorized && (c.authorizedTier != o.tier || c.authorizedMaximum != o.maximum)
	o.mu.Unlock()
	if changed {
		return networkHoldSourceError()
	}
	return ctx.Err()
}

func (c *networkHoldConfigV1) authorizeFile(ctx context.Context, tier Tier, maximum api.NetworkHoldMaximum) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkLocked(ctx, false); err != nil {
		return err
	}
	c.owner.mu.Lock()
	matches := c.owner.tier == tier && c.owner.maximum == maximum
	c.owner.mu.Unlock()
	if !matches {
		return networkHoldSourceError()
	}
	c.authorized, c.authorizedTier, c.authorizedMaximum = true, tier, maximum
	return nil
}
