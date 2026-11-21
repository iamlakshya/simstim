//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
)

// Used inside the still-held ordinary placement transaction on partial
// bootstrap failure. No OP loan or factory/claim could have been dispatched.
func (managed *noneManagerCustodyV1) settlePreloanV1() error {
	launch := managed.launch
	if launch == nil || !launch.started {
		managed.cancelled = true
		return nil
	}
	if launch.loanSent || managed.claimed {
		return sessionLeaseError("pre-loan None cleanup crossed a real loan or claim admission")
	}
	if launch.line != nil {
		_ = launch.line.Close()
		launch.readFailed = true
	}
	if launch.unit != nil && launch.unit.peer != nil && launch.unit.peer.identity != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if launch.unit.Check(ctx) == nil {
			var job dbus.ObjectPath
			_ = managed.manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".StopUnit", 0, launch.unit.unit, "fail").Store(&job)
		}
		cancel()
	}
	if err := launch.waitNativeExit(); err != nil && !launch.exited {
		return err
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var err error
		if managed.creation != nil && managed.group != nil {
			err = managed.checkBootstrapExitV1(ctx)
		} else if launch.loadedPreloanEnd {
			err = managed.manager.checkLoadedOriginalEndV1(ctx)
		} else {
			err = managed.checkPinnedV1(ctx)
			if err == nil {
				facts, readErr := readMemoryUnitV1(ctx, managed.operation.Primary(), false)
				err = readErr
				if facts.exists {
					err = errors.Join(err, sessionLeaseError("negative None dispatch still has a native ordinary lifetime"))
				}
			}
		}
		cancel()
		if err == nil {
			managed.cancelled = true
			return nil
		}
		// Keep the SAME placement transaction, manager and operation. An
		// unknown or replaced native owner cannot become zero RSS authority.
		time.Sleep(20 * time.Millisecond)
	}
}

func (managed *noneManagerCustodyV1) settleForCloseV1() error {
	if managed == nil || managed.self != managed || managed.custodyKernelSourceV1 == nil {
		return sessionLeaseError("the original managed None cleanup source is absent")
	}
	launch := managed.launch
	if launch == nil {
		return nil
	}
	if launch.ready {
		return launch.Close()
	}
	if !launch.loanSent {
		return managed.settlePreloanV1()
	}
	if !managed.cancelled {
		if launch.line != nil && !launch.cancelAttempted {
			launch.cancelAttempted = true
			if launch.custodyReleaseSent {
				_ = launch.cancelPending()
			} else {
				_ = launch.abortCustodyPassiveV1()
				_ = launch.line.Close()
				launch.readFailed = true
			}
		}
		if err := launch.waitNativeExit(); err != nil && !launch.exited {
			return err
		}
		if managed.creation == nil || managed.group == nil {
			return sessionLeaseError("the admitted None bootstrap lost its original native exit handles")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := managed.checkBootstrapExitV1(ctx)
		cancel()
		if err != nil {
			return err
		}
		if launch.custodyReleaseSent && !launch.cleanupConfirmed {
			return sessionLeaseError("the claimed None startup has no actual backend cleanup acknowledgement")
		}
		managed.cancelled = true
	}
	return nil
}

// Directory cleanup and a staged-writer rollback retain this distinct kernel
// source after client cancellation. It cannot issue another start or claim.
func (managed *noneManagerCustodyV1) checkRetainedCleanupV1(ctx context.Context) error {
	if managed == nil || managed.self != managed || managed.custodyKernelSourceV1 == nil || managed.cold == nil || managed.cold.managed != managed || managed.closed || managed.claimed || managed.cold.closed || !managed.operation.Valid(managed.operation.Primary()) || managed.boot != BootID() || len(managed.roles) != custodyBundleSizeV1 {
		return sessionLeaseError("the retained None cleanup lost its unconsumed original kernel source")
	}
	return errors.Join(CheckCaller(), managed.operation.native.check(ctx), managed.manager.checkNativeManager(), managed.checkRoleFactsV1(ctx), managed.checkOriginalHolderV1(ctx), ctx.Err())
}

func (cold *NoneColdResumeV1) closeManagedV1() (failure error) {
	cold.life.Lock()
	defer cold.life.Unlock()
	cold.mu.Lock()
	if cold.closed {
		cold.mu.Unlock()
		return nil
	}
	managed := cold.managed
	cold.mu.Unlock()
	if err := managed.settleForCloseV1(); err != nil {
		return err
	}
	if err := cold.restoreUnstartedMetadataV1(); err != nil {
		return err
	}
	if cold.directoryCleanup != nil {
		if err := cold.directoryCleanup.Close(); err != nil {
			return err
		}
	}
	if managed.launch != nil {
		if err := managed.launch.Close(); err != nil {
			return err
		}
	}
	if cold.metadata != nil {
		if err := cold.metadata.Close(); err != nil {
			return err
		}
		cold.metadata = nil
	}
	if err := managed.closeKernelV1(); err != nil {
		return err
	}
	cold.mu.Lock()
	cold.closed = true
	managed.closed = true
	cold.mu.Unlock()
	return nil
}
