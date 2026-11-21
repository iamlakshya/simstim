//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"
)

type purgeAuxiliaryKeyV1 struct{}

// This live restriction is minted only after the actual purge's physical stop
// and current native control check. It is never encoded or made from a report.
type purgeAuxiliaryPermitV1 struct {
	mu        sync.Mutex
	owner     *purgeOwnerV1
	operation *SessionOperation
	stopped   *StoppedSessionV1
	auxiliary *auxiliaryOperationV1
}

func withPurgeAuxiliaryContextV1(ctx context.Context, owner *purgeOwnerV1, operation *SessionOperation, stopped *StoppedSessionV1) (context.Context, error) {
	if ctx == nil || owner == nil || operation == nil || stopped == nil || stopped.operation != operation || stopped.origin != owner.placement {
		return ctx, sessionLeaseError("the purge inspection has no same physical stop and original controller")
	}
	if err := errors.Join(ctx.Err(), owner.ctx.Err(), stopped.Check(ctx, operation)); err != nil {
		return ctx, err
	}
	permit := &purgeAuxiliaryPermitV1{owner: owner, operation: operation, stopped: stopped}
	bounded := context.WithValue(ctx, purgeAuxiliaryKeyV1{}, permit)
	err := owner.placement.withControlRoot(ctx, func(control *placementControl, _ *os.Root) error {
		return checkPurgeAuxiliaryControlV1(bounded, operation, stopped, control)
	}, false)
	return bounded, err
}

// bindPurgeAuxiliaryOperationV1 consumes the ONE real auxiliary constructor's
// captured vacancy and artifact owner before publication or native dispatch.
// A second object cannot inherit this stopped source or removal permission.
func bindPurgeAuxiliaryOperationV1(ctx context.Context, auxiliary *auxiliaryOperationV1) error {
	if ctx == nil {
		return sessionLeaseError("the purge auxiliary has no original context lifetime")
	}
	permit, _ := ctx.Value(purgeAuxiliaryKeyV1{}).(*purgeAuxiliaryPermitV1)
	if permit == nil {
		return nil
	}
	if auxiliary == nil {
		return sessionLeaseError("the purge auxiliary has no actual native constructor")
	}
	if err := errors.Join(ctx.Err(), permit.owner.ctx.Err()); err != nil {
		return err
	}
	if auxiliary.operation != permit.operation || auxiliary.vacancy == nil || auxiliary.vacancy.operation != permit.operation || auxiliary.artifacts == nil || auxiliary.artifacts.vacancy != auxiliary.vacancy || auxiliary.artifacts.placement != permit.owner.placement {
		return sessionLeaseError("the purge inspection auxiliary changed its original native owner")
	}
	if err := errors.Join(permit.stopped.Check(ctx, permit.operation), auxiliary.vacancy.Check(ctx)); err != nil {
		return err
	}
	auxiliary.mu.Lock()
	used := auxiliary.dispatched || auxiliary.closed || auxiliary.settled
	auxiliary.mu.Unlock()
	if used {
		return sessionLeaseError("the purge inspection auxiliary was already dispatched or settled")
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.auxiliary != nil && permit.auxiliary != auxiliary {
		return sessionLeaseError("the purge inspection already retained another actual auxiliary")
	}
	permit.auxiliary = auxiliary
	return nil
}

func checkPurgeAuxiliaryAdmissionV1(ctx context.Context) error {
	if ctx == nil {
		return sessionLeaseError("the purge auxiliary has no original context lifetime")
	}
	permit, _ := ctx.Value(purgeAuxiliaryKeyV1{}).(*purgeAuxiliaryPermitV1)
	if permit == nil {
		return nil
	}
	if permit.owner == nil {
		return sessionLeaseError("the auxiliary has no original purge admission")
	}
	if err := errors.Join(ctx.Err(), permit.owner.ctx.Err()); err != nil {
		return err
	}
	permit.mu.Lock()
	auxiliary := permit.auxiliary
	permit.mu.Unlock()
	// This also runs inside a publication callback. The actual control record
	// is checked by withAuxiliaryPlacementV1; do not reacquire its native lock.
	return permit.stopped.checkPurgeAuxiliarySourceV1(ctx, permit.operation, auxiliary)
}

func checkPurgeAuxiliaryContextControlV1(ctx context.Context, control *placementControl) error {
	if ctx == nil {
		return sessionLeaseError("the purge auxiliary has no original context lifetime")
	}
	permit, _ := ctx.Value(purgeAuxiliaryKeyV1{}).(*purgeAuxiliaryPermitV1)
	if permit == nil {
		if control != nil && control.Drain == "" {
			return nil
		}
		return UserError("draining", "this host is draining for removal", "Finish the original removal before starting another auxiliary operation.")
	}
	return checkPurgeAuxiliaryControlV1(ctx, permit.operation, permit.stopped, control)
}

func checkPurgeAuxiliaryControlV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1, control *placementControl) error {
	if ctx == nil {
		return sessionLeaseError("the purge auxiliary has no original context lifetime")
	}
	permit, _ := ctx.Value(purgeAuxiliaryKeyV1{}).(*purgeAuxiliaryPermitV1)
	if permit == nil || permit.owner == nil || permit.operation != operation || permit.stopped != stopped || operation == nil || stopped == nil || control == nil {
		return sessionLeaseError("the auxiliary drain exception has no same original native subject")
	}
	owner := permit.owner
	if control.HostID != owner.args.HostID || control.Scope != owner.placement.scope || control.Nonce != owner.args.Nonce || control.Drain != "purge" || control.Phase != "precheck" || control.OwnerRun != owner.run || control.Owner == nil || *control.Owner != owner.process || stopped.operation != operation || stopped.origin != owner.placement {
		return sessionLeaseError("the auxiliary lost its exact original purge control owner")
	}
	if plan, known := control.Sessions[operation.Primary()]; known && plan.Cleaned {
		return sessionLeaseError("the original purge already removed this session generation")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	permit.mu.Lock()
	auxiliary := permit.auxiliary
	permit.mu.Unlock()
	if owner.ctx.Err() != nil {
		if auxiliary == nil {
			return owner.ctx.Err()
		}
		auxiliary.mu.Lock()
		cleanupOnly := auxiliary.settled || !auxiliary.dispatched
		auxiliary.mu.Unlock()
		if !cleanupOnly {
			return owner.ctx.Err()
		}
	}
	// The real native source remains the authority. In particular the settled
	// and pre-dispatch flags above do not establish native absence: this checks
	// original kernel objects plus this exact owned auxiliary counterpart again.
	return stopped.checkPurgeAuxiliarySourceV1(ctx, operation, auxiliary)
}

// New native effects use this callback INSIDE the current placement gate.
// Cleanup uses the narrower context/control predicate after genuine settlement.
func checkPurgeAuxiliaryAdmissionControlV1(ctx context.Context, control *placementControl) error {
	if ctx == nil {
		return sessionLeaseError("the purge auxiliary has no original context lifetime")
	}
	permit, _ := ctx.Value(purgeAuxiliaryKeyV1{}).(*purgeAuxiliaryPermitV1)
	if permit != nil {
		if permit.owner == nil {
			return sessionLeaseError("the original purge admission owner ended")
		}
		if err := errors.Join(ctx.Err(), permit.owner.ctx.Err()); err != nil {
			return err
		}
	}
	return checkPurgeAuxiliaryContextControlV1(ctx, control)
}
