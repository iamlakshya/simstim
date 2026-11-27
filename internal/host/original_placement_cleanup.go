package host

import "context"

// BootPlacementCleanupV1 retains only cleanup access to the actual placement
// captured by an accepted native startup loan. It cannot admit any work.
type BootPlacementCleanupV1 struct {
	placement *placement
}

func admittedBootPlacementV1(ctx context.Context) (*placement, error) {
	if ctx == nil {
		return nil, sessionLeaseError("the original boot placement has no owner context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	placementProcesses.Lock()
	p := placementProcesses.entry
	placementProcesses.Unlock()
	if p == nil || p.installer {
		return nil, placementError("the original boot has no admitted native process placement")
	}
	if err := p.checkIdentity(); err != nil {
		return nil, err
	}
	return p, ctx.Err()
}

// PlacementCleanupV1 must be retained before runtime preparation. Only the
// same accepted loan's live OP and supervisor can issue this cleanup handle.
func (owner *OwnedSupervisorLease) PlacementCleanupV1(operation *SessionOperation) (*BootPlacementCleanupV1, error) {
	if owner == nil || operation == nil {
		return nil, sessionLeaseError("cleanup has no original native boot owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || boot.owner != owner || boot.operation != operation || operation.originalBoot != boot ||
		boot.placement == nil || !owner.Valid(boot.loan.Primary) || !operation.Valid(boot.loan.Primary) {
		return nil, sessionLeaseError("cleanup differs from the accepted original boot loan")
	}
	return &BootPlacementCleanupV1{placement: boot.placement}, nil
}

// CloseContextV1 retries only the actual retained failed constructor. It stays
// usable after cancellation; success is required before OP/holder retirement.
// This does not establish sandbox death or acknowledge preparation cleanup.
func (cleanup *BootPlacementCleanupV1) CloseContextV1(ctx context.Context) error {
	if cleanup == nil || cleanup.placement == nil || ctx == nil {
		return placementError("cleanup has no retained original placement")
	}
	return cleanup.placement.retryPlacementConstructorV1(ctx)
}
