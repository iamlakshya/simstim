package client

import "context"

// SameShellSandboxV1 restricts selection to this laptop's existing SAME host
// and lineage. It grants no value, fetch or new child authority. The actual
// shell start still uses StartFreshChild and its immutable original factory.
func (c *Conn) SameShellSandboxV1(ctx context.Context, parentID, childID string) (bool, error) {
	if c == nil {
		return false, joinIntentError()
	}
	if err := c.BindHostStateObservation(ctx); err != nil {
		return false, err
	}
	parent, err := LoadSandboxPolicy(ctx, c.Host, parentID)
	if err != nil {
		return false, err
	}
	child, err := LoadSandboxPolicy(ctx, c.Host, childID)
	if err != nil {
		return false, err
	}
	if parent == nil || child == nil || parent.FutureLineage || parent.FutureDelivery || child.FutureLineage || child.FutureDelivery || parent.HostID != c.HostID() || child.HostID != c.HostID() || parent.CreationWall == "none" || parent.CreationWall != child.CreationWall {
		return false, nil
	}
	return localSandboxRoot(parent) == localSandboxRoot(child), c.BindHostStateObservation(ctx)
}
