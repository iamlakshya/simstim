//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

func (binding *slotSandboxBinding) withCheckpointBackendV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(checkpoint.BackendV1) error) (returned error) {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.proof.mu.Lock()
	source := binding.proof.source
	binding.proof.mu.Unlock()
	custody, err := source.RetainCheckpointCustodyV1(ctx)
	if custody != nil {
		defer func() { returned = errors.Join(returned, binding.closeOrKeepSlotOwnerV1(custody)) }()
	}
	if err != nil {
		return err
	}
	backend, err := custody.CheckpointBackendV1(ctx)
	if err != nil {
		return err
	}
	if created, ok := backend.(interface{ CloseCreatedV1() error }); ok {
		defer func() { returned = errors.Join(returned, binding.closeOrKeepSlotOwnerV1(slotCreatedOwnerV1{created})) }()
	}
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	result := borrow(backend)
	return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
}

type slotCreatedOwnerV1 struct {
	original interface{ CloseCreatedV1() error }
}

func (owner slotCreatedOwnerV1) Close() error { return owner.original.CloseCreatedV1() }

func (binding *slotSandboxBinding) withCheckpointLoanV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, borrow func(CheckpointLoanV1) error) (returned error) {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.proof.mu.Lock()
	source := binding.proof.source
	binding.proof.mu.Unlock()
	custody, err := source.RetainCheckpointCustodyV1(ctx)
	if custody != nil {
		defer func() { returned = errors.Join(returned, binding.closeOrKeepSlotOwnerV1(custody)) }()
	}
	if err != nil {
		return err
	}
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	result := borrow(custody)
	return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
}

func (binding *slotSandboxBinding) copyForkDiskV1(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, target *os.File) error {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.proof.mu.Lock()
	source := binding.proof.source
	binding.proof.mu.Unlock()
	if source == nil {
		return fmt.Errorf("the original slot fork has no root-issued source")
	}
	result := source.CopyForkDiskToV1(ctx, target)
	return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
}

func (binding *slotSandboxBinding) withCheckpointReservationLoanV2(ctx context.Context, connection net.Conn, expected vmneed.NeedV1, use func(CheckpointReservationLoanV2) error) (returned error) {
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	binding.proof.mu.Lock()
	source := binding.proof.source
	binding.proof.mu.Unlock()
	custody, err := source.RetainCheckpointCustodyV1(ctx)
	if custody != nil {
		defer func() { returned = errors.Join(returned, binding.closeOrKeepSlotOwnerV1(custody)) }()
	}
	if err != nil {
		return err
	}
	reservation, err := custody.CaptureReservationLoanV2(ctx)
	if reservation != nil {
		defer func() { returned = errors.Join(returned, binding.closeOrKeepSlotOwnerV1(reservation)) }()
	}
	if err != nil {
		return err
	}
	if err := binding.checkNativeBinding(ctx, connection, expected); err != nil {
		return err
	}
	result := use(reservation)
	return errors.Join(result, binding.checkNativeBinding(ctx, connection, expected))
}
