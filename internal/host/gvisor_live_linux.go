//go:build linux

package host

import (
	"context"
	"errors"
	"reflect"
)

// CheckNativeLiveV1 joins the concrete original Cmd's creation handles with
// this host-issued immutable boot/runtime source and its actual holder. The
// borrowed startup OP closes at the ready commit; it cannot be renewed from
// the saved row or used as the source of a later warm request.
func (command *originalGvisorCommandV1) CheckNativeLiveV1(ctx context.Context) error {
	if command == nil || command.Cmd == nil || command.ctx == nil || ctx == nil || command.receipt == nil {
		return vmNeedError("the original gvisor command source is absent")
	}
	if err := errors.Join(ctx.Err(), command.ctx.Err()); err != nil {
		return err
	}
	receipt := OriginalVMNeedFromContext(command.ctx)
	if receipt != command.receipt || receipt == nil || receipt.boot == nil || receipt.boot.owner == nil {
		return vmNeedError("the original gvisor boot receipt changed")
	}
	owner := receipt.boot.owner
	owner.mu.Lock()
	live := owner.startup == receipt.boot && owner.native != nil && owner.Valid(receipt.boot.loan.Primary) && receipt.boot.gvisorIssued && receipt.need == nil && len(receipt.rawNeed) == 0 && receipt.boot.runtime != nil && command.spec.RuntimeManifest != nil && reflect.DeepEqual(*receipt.boot.runtime, *command.spec.RuntimeManifest) && command.spec.ID == receipt.boot.loan.Primary && command.spec.MemoryMax == receipt.spec.MemoryMax
	ready := receipt.boot.ready
	owner.mu.Unlock()
	if !live {
		return vmNeedError("the original gvisor native owner ended or changed")
	}
	if !ready {
		// Before commit the actual original startup OP is still mandatory.
		before, err := checkOriginalGvisorSpecV1(command.ctx, command.spec)
		if err != nil {
			return err
		}
		if before != receipt {
			return vmNeedError("the original pending gvisor boot source changed")
		}
	}
	return errors.Join(command.Cmd.CheckNativeLiveV1(ctx), ctx.Err(), command.ctx.Err())
}
