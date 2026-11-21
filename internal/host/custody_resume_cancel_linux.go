//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

func (source *managerCustodyColdV1) SettleForCancellationV1(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.custodyKernelSourceV1 == nil || operation == nil || operation != source.operation || !operation.Valid(operation.Primary()) {
		return auxiliaryUnavailableV1("custody cancellation lost the SAME original operation")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return auxiliaryUnavailableV1("the actual custody source closed before cancellation")
	}
	if source.launch != nil && source.launch.ready {
		return auxiliaryUnavailableV1("the committed runtime retains its actual capacity owner")
	}
	if source.cancelled {
		return source.checkCancelledV1(ctx)
	}
	return source.cancelBootstrapV1(ctx)
}

func (source *managerCustodyColdV1) cancelBootstrapV1(ctx context.Context) error {
	launch := source.launch
	if launch == nil || !launch.started {
		source.cancelled = true
		return nil
	}
	if err := source.manager.checkNativeManager(); err != nil {
		return err
	}
	if launch.loanSent && !launch.custodyReleaseSent && launch.line != nil {
		_ = launch.abortCustodyPassiveV1()
	}
	if launch.custodyReleaseSent {
		if launch.line != nil && !launch.cancelAttempted {
			launch.cancelAttempted = true
			_ = launch.cancelPending()
		}
		if !launch.cleanupConfirmed {
			return auxiliaryUnavailableV1("the released startup has no original backend cleanup acknowledgement")
		}
	}
	if !launch.loanSent && !launch.custodyReleaseSent && launch.unit != nil && launch.unit.peer.checkLive() == nil {
		err := launch.unit.checkCreationV1(ctx)
		if launch.unit.peer.identity != nil {
			err = launch.unit.Check(ctx)
		}
		if err != nil {
			return err
		}
		var job dbus.ObjectPath
		if err := source.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".StopUnit", 0, launch.unit.unit, "fail").Store(&job); err != nil && launch.unit.peer.checkLive() == nil {
			return err
		}
	}
	// Closing the SAME physical line cancels the passive borrower without
	// consuming its claim. The original native waiter must still join.
	if launch.line != nil {
		if err := launch.line.Close(); err != nil {
			return err
		}
		launch.readFailed = true
	}
	if err := launch.waitNativeExit(); err != nil {
		return err
	}
	if launch.unit != nil && !launch.loadedPreloanEnd && (source.creation == nil || source.group == nil) {
		return auxiliaryUnavailableV1("bootstrap cancellation lost its original native exit handles")
	}
	if source.creation != nil {
		for {
			if err := source.checkBootstrapExitV1(ctx); err == nil {
				break
			}
			if err := source.manager.checkNativeManager(); err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	source.cancelled = true
	if err := source.checkCancelledV1(ctx); err != nil {
		source.cancelled = false
		return err
	}
	return nil
}

func (source *custodyKernelSourceV1) checkBootstrapExitV1(ctx context.Context) error {
	if source.creation == nil || source.group == nil {
		return auxiliaryUnavailableV1("the actual bootstrap exit descriptors are absent")
	}
	polls := []unix.PollFd{{Fd: int32(source.creation.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(polls, 0)
	if err != nil || n != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(auxiliaryUnavailableV1("the original bootstrap has no positive native exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(source.group)
	if err != nil || !empty {
		return errors.Join(auxiliaryUnavailableV1("the original bootstrap cgroup still has native writers"), err)
	}
	return errors.Join(ctx.Err(), source.manager.checkNativeManager())
}

func (source *managerCustodyColdV1) checkCancelledV1(ctx context.Context) error {
	if !source.cancelled || source.closed || source.operation == nil || !source.operation.Valid(source.operation.Primary()) {
		return auxiliaryUnavailableV1("the original custody cancellation has no settled owner")
	}
	if source.launch != nil && source.launch.started && source.creation != nil {
		if err := source.checkBootstrapExitV1(ctx); err != nil {
			return err
		}
	}
	if source.launch != nil && source.launch.loadedPreloanEnd {
		if !source.launch.canReconcileLoadedPreloanV1() {
			return auxiliaryUnavailableV1("the loaded cleanup crossed an actual startup loan admission")
		}
		return errors.Join(source.manager.checkLoadedOriginalEndV1(ctx), source.operation.native.check(ctx))
	}
	if source.launch != nil && source.launch.started && source.creation == nil {
		// A genuine negative StartUnit reply does not exclude a concurrent
		// manual activation. Keep the full charge until the same canonical
		// retained service is inactive and its native cgroup has gone.
		if err := source.checkPinnedV1(ctx); err != nil {
			return err
		}
		facts, err := readMemoryUnitV1(ctx, source.operation.Primary(), true)
		if err != nil || facts.exists {
			return errors.Join(auxiliaryUnavailableV1("negative bootstrap dispatch still has a canonical native lifetime"), err)
		}
	}
	if !source.claimed && len(source.roles) == custodyBundleSizeV1 {
		if err := source.checkPinnedV1(ctx); err != nil {
			return err
		}
		return source.checkRoleFactsV1(ctx)
	}
	// A dispatched claim with a lost ACK is never renewed. Real bootstrap
	// closure can settle its capacity charge, while the consumed old bundle
	// remains fenced until explicit repair. No CleanUnit or holder reopen.
	return errors.Join(source.operation.native.check(ctx), source.manager.checkNativeManager(), ctx.Err())
}
