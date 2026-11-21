//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// SettleForCancellationV1 preserves this original cold source through actual
// failed-new-start cleanup. It never stops a committed ready runtime. A new
// genuine holder vacancy is acquired only after the tracked creation and
// BOTH native unit/cgroup lifetimes have ended under the SAME original OP.
func (cold *ColdResumeV1) SettleForCancellationV1(ctx context.Context, operation *SessionOperation) error {
	if cold == nil || ctx == nil || operation == nil || operation != cold.operation || cold.vacancy == nil || cold.artifacts == nil {
		return auxiliaryUnavailableV1("cold cancellation has no original native source")
	}
	cold.life.Lock()
	defer cold.life.Unlock()
	cold.mu.Lock()
	if cold.closed {
		cold.mu.Unlock()
		return auxiliaryUnavailableV1("the original cold source closed before cancellation settled")
	}
	if !cold.handed {
		err := cold.checkLockedV1(ctx, operation)
		cold.mu.Unlock()
		return err
	}
	launch := cold.startup
	cold.mu.Unlock()
	if launch == nil || operation.startup != launch || launch.operation != operation {
		return auxiliaryUnavailableV1("cold cancellation lost its actual new native creation")
	}
	launch.mu.Lock()
	ready := launch.ready
	launch.mu.Unlock()
	cold.mu.Lock()
	creation := cold.creation
	cold.mu.Unlock()
	if ready {
		return auxiliaryUnavailableV1("the committed native runtime must retain its actual capacity owner")
	}
	if err := launch.Close(); err != nil {
		return err
	}
	if launch.unit != nil && creation == nil {
		return auxiliaryUnavailableV1("the original cold creation descriptor was not retained")
	}
	if creation != nil {
		polls := []unix.PollFd{{Fd: int32(creation.Fd()), Events: unix.POLLIN}}
		count, err := unix.Poll(polls, 0)
		if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
			return errors.Join(auxiliaryUnavailableV1("the actual cold creation has no positive native exit"), err)
		}
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if cold.closed || cold.startup != launch || !cold.handed || !operation.Valid(operation.Primary()) {
		return auxiliaryUnavailableV1("the original cold cancellation source changed")
	}
	vacancy := cold.vacancy
	manager := vacancy.manager
	if manager == nil {
		return auxiliaryUnavailableV1("cold cancellation has no original native manager")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := manager.checkNativeManager(); err != nil {
			return err
		}
		err := manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.primary)
		if err == nil {
			err = manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.auxiliary)
		}
		ordinary, ordinaryErr := readMemoryUnitV1(ctx, operation.Primary(), true)
		auxiliary, auxiliaryErr := readAuxMemoryUnitV1(ctx, operation.Primary())
		if err == nil && ordinaryErr == nil && auxiliaryErr == nil && !ordinary.exists && !auxiliary.exists {
			if err = manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.primary); err == nil {
				err = manager.requireAuxiliaryUnitAbsentV1(ctx, vacancy.auxiliary)
			}
			if err == nil {
				break
			}
		}
		if err := manager.checkNativeManager(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	holder, err := openOwnedSessionFileLease(ctx, operation.Primary(), holderLockFile, 0)
	if err != nil {
		return err
	}
	if err := operation.native.rootRef.CheckRoot(holder.root); err != nil {
		return errors.Join(err, holder.Close())
	}
	vacancy.mu.Lock()
	vacancy.holder = holder
	vacancy.transferred = false
	vacancy.mu.Unlock()
	cold.handed = false
	cold.startup = nil
	return cold.checkLockedV1(ctx, operation)
}
