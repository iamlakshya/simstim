package host

import (
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/wall/none"
)

// noneHolderBirthV1 is minted only while binding this SAME actual original
// None birth, outside startup loan and held native holder. It is never data.
type noneHolderBirthV1 struct {
	self       *noneHolderBirthV1
	receipt    *OriginalVMNeed
	owner      *OwnedSupervisorLease
	creator    *pendingStartupV1
	birth      *none.LiveOwnerV1
	connection net.Conn
	holder     *sessionFileLease
	file       *os.File
	generation string
}

// The real receipt/owner locks are already held by BindOriginalNoneBirthV1.
func newNoneHolderBirthLockedV1(ctx context.Context, receipt *OriginalVMNeed, birth *none.LiveOwnerV1, connection net.Conn) (*noneHolderBirthV1, error) {
	if ctx == nil || receipt == nil || receipt.boot == nil || receipt.boot.owner == nil || birth == nil || connection == nil {
		return nil, sessionLeaseError("the None holder has no original startup birth")
	}
	owner := receipt.boot.owner
	if owner.startup != receipt.boot || receipt.boot.ready || owner.pending == nil || owner.pending.peer == nil || receipt.boot.line == nil || receipt.boot.operation == nil || receipt.boot.operation.native == nil || !receipt.boot.operation.Valid(receipt.boot.loan.Primary) || owner.native == nil || owner.native.name != holderLockFile || !owner.Valid(receipt.boot.loan.Primary) || owner.generation == "" || !validNoneMigrationBootV1(BootID()) {
		return nil, sessionLeaseError("the None holder differs from its actual outside creator and SAME operation")
	}
	if receipt.boot.line.isClosed() {
		return nil, sessionLeaseError("the original startup connection closed before None holder birth")
	}
	sender := receipt.boot.sender
	if err := errors.Join(owner.pending.peer.CheckSender(sender), checkBorrowedFlockV1(receipt.boot.operation.native.file, sender), birth.CheckSessionV1(ctx, receipt.boot.loan.Primary, connection), observeNoneHolderV1(owner.native.file), owner.native.check(ctx), ctx.Err()); err != nil {
		return nil, err
	}
	result := &noneHolderBirthV1{receipt: receipt, owner: owner, creator: owner.pending, birth: birth, connection: connection, holder: owner.native, file: owner.native.file, generation: owner.generation}
	result.self = result
	return result, nil
}

// Original creator liveness was checked at issuance. A successfully committed
// outside bridge may have exited: this check uses the SAME current None
// actor, actual live/cleaned birth, original Start connection and held OFD.
// It never changes borrowed, reacquires, unlocks or reopens a description.
func (origin *noneHolderBirthV1) checkDescriptionLockedV1(ctx context.Context, receipt *OriginalVMNeed, birth *none.LiveOwnerV1) error {
	if origin == nil || origin.self != origin || ctx == nil || receipt == nil || origin.receipt != receipt || receipt.noneHolder != origin || receipt.boot == nil || receipt.boot.owner != origin.owner || origin.owner == nil || origin.owner.startup != receipt.boot || origin.owner.pending != origin.creator || origin.creator == nil || !receipt.boot.ready || origin.birth != birth || receipt.noneBirth != birth || origin.connection == nil || receipt.noneConnection != origin.connection || origin.holder != origin.owner.native || origin.file == nil || origin.holder == nil || origin.holder.file != origin.file || origin.holder.name != holderLockFile || origin.generation == "" || origin.owner.generation != origin.generation || !origin.owner.Valid(receipt.boot.loan.Primary) || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.boot.runtime != nil || receipt.spec.VM != nil || receipt.spec.RuntimeManifest != nil {
		return sessionLeaseError("the original None holder birth ended, was copied or changed its actual owner")
	}
	if err := errors.Join(origin.holder.check(ctx), observeNoneHolderV1(origin.file), ctx.Err()); err != nil {
		return err
	}
	return nil
}

func (origin *noneHolderBirthV1) checkLiveLockedV1(ctx context.Context, receipt *OriginalVMNeed, birth *none.LiveOwnerV1) error {
	if err := origin.checkDescriptionLockedV1(ctx, receipt, birth); err != nil {
		return err
	}
	return birth.CheckSessionV1(ctx, receipt.boot.loan.Primary, origin.connection)
}

func (origin *noneHolderBirthV1) checkCleanupLockedV1(ctx context.Context, receipt *OriginalVMNeed, birth *none.LiveOwnerV1) error {
	if err := origin.checkDescriptionLockedV1(ctx, receipt, birth); err != nil {
		return err
	}
	_, _, err := birth.CleanupObservationV1()
	return err
}
