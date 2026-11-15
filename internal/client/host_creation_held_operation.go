package client

import "context"

// Bind the SAME actually held native OP after LockHost, including its genuine
// reentrant path. Nothing opens/replaces an OP, refreshes a Source or adopts a
// dead/terminal/mismatched native owner. The original unlock stays its owner.
func bindHostCreationHeldOperation(ctx context.Context, observation *HostStateObservation) error {
	if observation == nil || observation.role != hostStateCreation || observation.creation == nil {
		return hostStateRemovalError("creation operation binding has no original reservation")
	}
	held, _ := ctx.Value(heldKey{observation.name}).(*heldLock)
	if held == nil || held.terminal.Load() {
		return hostStateRemovalError("creation operation binding lost its actual native owner")
	}
	if err := held.checkStateOperation(ctx); err != nil {
		return err
	}
	return withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := observation.checkMembership(locked); err != nil {
			return err
		}
		if !sameHostTransportSource(observation.source, held.native.source) {
			return hostStateRemovalError("the pre-held creation OP belongs to an older original Source")
		}
		if !observation.creation.op.CompareAndSwap(nil, held) && observation.creation.op.Load() != held {
			return hostStateRemovalError("the creation reservation already belongs to another physical OP")
		}
		return held.checkStateOperation(locked)
	})
}
