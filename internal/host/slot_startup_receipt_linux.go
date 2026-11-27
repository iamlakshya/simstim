//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

func (input *originalSlotStartupV2) checkScopeV2(ctx context.Context, receipt *OriginalVMNeed) error {
	if input == nil || input.issuer != input || input.incoming == nil || !input.incoming.used || input.boot == nil || input.boot.slotReservation != input || receipt == nil || receipt.boot != input.boot || input.operation == nil || input.operation.originalBoot != input.boot || input.incoming.owner != input.boot.owner || input.incoming.line != input.boot.line || input.incoming.pending == nil {
		return vmNeedError("the reserved creator has no actual accepted startup source")
	}
	boot := input.boot
	owner := boot.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup != boot || owner.pending != input.incoming.pending || owner.pending.line != boot.line || owner.pending.peer == nil || !owner.Valid(boot.loan.Primary) || !input.operation.Valid(boot.loan.Primary) || input.operation.native == nil || !input.operation.native.borrowed || !bytes.Equal(receipt.rawNeed, boot.currentNeed) {
		return vmNeedError("the original reserved startup operation or current receipt ended")
	}
	return errors.Join(ctx.Err(), owner.pending.peer.Check(), input.operation.native.check(ctx))
}

// HasOriginalCheckpointReservationV2 is selection DATA only. The actual
// provider and fixed root creator still consume the original native input.
func (receipt *OriginalVMNeed) HasOriginalCheckpointReservationV2(ctx context.Context) (bool, error) {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt {
		return false, vmNeedError("the reservation selector has no original boot receipt")
	}
	if receipt.boot.slotReservation == nil {
		return false, nil
	}
	input := receipt.boot.slotReservation
	input.mu.Lock()
	defer input.mu.Unlock()
	return true, input.checkScopeV2(ctx, receipt)
}

// WithOriginalCheckpointReservationV2 lends only this accepted boot's actual
// eighteen old native operands to its ONE fixed new root creator. A failed or
// uncertain callback cannot select ordinary allocation as a fallback.
func (receipt *OriginalVMNeed) WithOriginalCheckpointReservationV2(ctx context.Context, use func([]*os.File) error) (bool, error) {
	if receipt == nil || OriginalVMNeedFromContext(ctx) != receipt {
		return false, vmNeedError("the reserved creator has no original current boot receipt")
	}
	input := receipt.boot.slotReservation
	if input == nil {
		return false, nil
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	if use == nil {
		return true, vmNeedError("the actual reserved creator has no consumer")
	}
	if err := input.checkScopeV2(ctx, receipt); err != nil {
		return true, err
	}
	if input.attempted || input.released {
		return true, vmNeedError("the original reserved native creation was already attempted")
	}
	if err := input.checkClosedOperandsV2(ctx); err != nil {
		return true, err
	}
	input.attempted = true // An uncertain real native dispatch cannot be replayed.
	result := use(append([]*os.File(nil), input.roles...))
	return true, errors.Join(result, input.checkScopeV2(ctx, receipt))
}

// The caller holds receipt.prepareMu after the actual connection/echo/native
// binding checks. The new root keeper's actual loan, rather than its ready
// acknowledgement, proves the local old operands can now retire.
func (receipt *OriginalVMNeed) finishOriginalCheckpointReservationV2(ctx context.Context, connection net.Conn) error {
	input := receipt.boot.slotReservation
	if input == nil {
		return nil
	}
	input.mu.Lock()
	defer input.mu.Unlock()
	if err := input.checkScopeV2(ctx, receipt); err != nil {
		return err
	}
	if !input.attempted || input.released || receipt.binding == nil || receipt.need == nil || connection == nil {
		return vmNeedError("the reserved startup has no actual new native binding to consume its lease")
	}
	result := receipt.binding.WithCheckpointReservationLoanV2(ctx, connection, *receipt.need, func(loan microvm.CheckpointReservationLoanV2) error {
		return loan.WithLoanFilesV2(ctx, func(files []*os.File) error {
			if len(files) != 18 {
				return vmNeedError("the actual new keeper returned another reserved native vector")
			}
			if err := checkSameSlotLeaseOFDV2(input.roles[17], files[17]); err != nil {
				return err
			}
			// The actual new Source retains its own private lease and original birth
			// handles. Only now may this boot retire its copied old public operands.
			return input.closeRolesV2()
		})
	})
	return result
}
