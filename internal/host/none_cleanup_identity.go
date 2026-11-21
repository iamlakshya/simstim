package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/wall/none"
)

// NoneCleanupIdentitiesV1 projects DATA from this SAME original None birth and
// retained native holder. Neither returned reference issues any capability.
func (owner *OwnedSupervisorLease) NoneCleanupIdentitiesV1(ctx context.Context, birth *none.LiveOwnerV1) (cachefs.Reference, cachefs.Reference, error) {
	empty := cachefs.Reference{}
	receipt := OriginalVMNeedFromContext(ctx)
	if owner == nil || birth == nil || receipt == nil || receipt.boot == nil || receipt.boot.owner != owner {
		return empty, empty, sessionLeaseError("the None cleanup has no original birth and holder owner")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if receipt.noneBirth != birth || receipt.noneConnection == nil || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.spec.VM != nil || receipt.boot.runtime != nil || owner.startup != receipt.boot || !receipt.boot.ready || !owner.Valid(receipt.boot.loan.Primary) || owner.native == nil || owner.native.name != holderLockFile {
		return empty, empty, sessionLeaseError("the original None cleanup holder changed its role")
	}
	if _, _, err := birth.CleanupObservationV1(); err != nil {
		return empty, empty, err
	}
	if err := receipt.noneHolder.checkCleanupLockedV1(ctx, receipt, birth); err != nil {
		return empty, empty, err
	}
	if err := owner.native.check(ctx); err != nil {
		return empty, empty, err
	}
	return owner.native.rootRef, owner.native.fileRef, nil
}
