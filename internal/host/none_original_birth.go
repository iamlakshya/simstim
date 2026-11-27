package host

import (
	"context"
	"net"

	"github.com/iamlakshya/simstim/internal/wall/none"
)

// BindOriginalNoneBirthV1 consumes only the original native startup receipt
// and this actual created none runtime. A role DTO, context alone, later path
// or copied actor cannot construct an alternative native birth.
func (receipt *OriginalVMNeed) BindOriginalNoneBirthV1(ctx context.Context, birth *none.LiveOwnerV1, connection net.Conn) error {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || birth == nil || connection == nil {
		return sessionLeaseError("the none birth has no original native startup receipt")
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	owner := receipt.boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup != receipt.boot || !owner.Valid(receipt.boot.loan.Primary) || receipt.boot.ready || receipt.noneBirth != nil || receipt.need != nil || len(receipt.rawNeed) != 0 || receipt.boot.runtime != nil || receipt.spec.VM != nil || receipt.spec.RuntimeManifest != nil || !receipt.boot.operation.Valid(receipt.boot.loan.Primary) {
		return sessionLeaseError("the original none startup was changed, prepared as a VM, or already consumed")
	}
	manifest, err := receipt.boot.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !NoneSupervisorShapeV1(manifest) {
		return sessionLeaseError("the original native startup does not select this unwalled creator")
	}
	if err := birth.CheckSessionV1(ctx, receipt.boot.loan.Primary, connection); err != nil {
		return err
	}
	holder, err := newNoneHolderBirthLockedV1(ctx, receipt, birth, connection)
	if err != nil {
		return err
	}
	receipt.noneBirth, receipt.noneConnection = birth, connection
	receipt.noneHolder = holder
	return nil
}
