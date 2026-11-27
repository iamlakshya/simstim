package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"reflect"

	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// RunningAccountModeForWarmV1 observes the actual prepared runtime on its
// SAME accepted current Warm lane. The startup-only observation retains its
// pending-ready guard; a retired startup OP cannot authorise this operation.
// This result is routing data and supplies no Source or execution grant.
func (receipt *OriginalVMNeed) RunningAccountModeForWarmV1(ctx context.Context, runtime wall.Sandbox, connection net.Conn, owner *OwnedSupervisorLease, operation *SessionOperation, line *StartupLineV1) (bool, error) {
	if ctx == nil || ctx.Err() != nil || receipt == nil || OriginalVMNeedFromContext(ctx) != receipt || receipt.boot == nil || receipt.boot.owner != owner || owner == nil || owner.native == nil || operation == nil || operation.native == nil || !operation.native.borrowed || line == nil || runtime == nil || connection == nil {
		return false, vmNeedError("the running account observation has no original native Warm producer")
	}
	if err := errors.Join(owner.CheckWarmOperationV1(ctx, line, operation), operation.native.check(ctx)); err != nil {
		return false, err
	}
	receipt.prepareMu.Lock()
	defer receipt.prepareMu.Unlock()
	if receipt.need == nil || !receipt.prepareUsed || !receipt.prepareConfirmed || receipt.binding == nil || receipt.connection != connection {
		return false, vmNeedError("the current Warm account observation lost its original prepared runtime")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := receipt.boot
	if owner.startup != boot || !boot.ready || !owner.Valid(boot.loan.Primary) || !operation.Valid(boot.loan.Primary) || operation.Primary() != owner.native.id {
		return false, vmNeedError("the account observation is outside its actual committed native run")
	}
	if err := owner.native.rootRef.CheckRoot(operation.native.root); err != nil {
		return false, err
	}
	saved, err := operation.LoadContext(ctx)
	if err != nil {
		return false, err
	}
	if saved.Life == nil || saved.Life.Phase != LifeRunning || saved.Life.Started.IsZero() || !saved.Life.Started.Equal(saved.Run.StartedAt) || saved.Run.Generation != owner.generation || saved.Run.HolderPID != os.Getpid() || saved.Run.BootID != BootID() || !bytes.Equal(saved.VMNeedV1, receipt.rawNeed) || !bytes.Equal(boot.currentNeed, receipt.rawNeed) || !reflect.DeepEqual(saved.RuntimeManifest, boot.runtime) || receipt.binding.SessionID() != saved.ID {
		return false, vmNeedError("the running account observation changed its original protected native run")
	}
	return microvm.OriginalAccountModeV1(ctx, runtime, connection, receipt.binding, *receipt.need)
}
