//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

// Close keeps the actual outside OP, source descriptors, original manager and
// reservation until the new helper AND its whole native unit are absent.
// A wire acknowledgement, EOF, cancellation or main PID exit is insufficient.
func (owner *auxiliaryOperationV1) Close() error {
	if owner == nil {
		return nil
	}
	owner.closeMu.Lock()
	defer owner.closeMu.Unlock()
	owner.mu.Lock()
	dispatched, closed := owner.dispatched, owner.closed
	owner.mu.Unlock()
	if closed {
		return owner.closeErr
	}
	if !dispatched {
		return owner.closeUnstarted()
	}
	if owner.transport != nil {
		_ = owner.transport.Close()
	}
	if owner.line != nil {
		_ = owner.line.Close()
	}
	manager := owner.vacancy.manager
	if err := manager.checkNativeManager(); err != nil {
		return owner.retainUncertainAuxiliaryV1(err)
	}
	// If startup failed before the native peer was pinned, resolve only the
	// SAME returned original job. Never StopUnit a colliding unknown activation.
	if owner.activation == nil {
		if owner.job != "" {
			if err := owner.resolveAuxiliaryActivationV1(context.Background()); err != nil {
				if err := manager.events.waitOriginalEnd(); err != nil {
					return owner.retainUncertainAuxiliaryV1(err)
				}
			}
		} else if err := manager.events.waitOriginalEnd(); err != nil {
			return owner.retainUncertainAuxiliaryV1(err)
		}
	}
	// StopUnit is confined to the first activation observed on THIS original
	// authenticated manager. A competing/replaced invocation is not ours to kill.
	if owner.activation != nil {
		if err := owner.activation.peer.checkLive(); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = owner.activation.Check(ctx)
			cancel()
			if err != nil {
				return owner.retainUncertainAuxiliaryV1(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var stopErr error
	if owner.activation != nil {
		var job dbus.ObjectPath
		call := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".StopUnit", 0, owner.vacancy.auxiliary, "fail")
		stopErr = call.Store(&job)
	}
	cancel()
	// The native source stack stays alive during uncertain startup/stop. Only
	// the original manager's negative creation reply or correlated unit removal
	// may settle a dispatch without an observed actual creation pidfd.
	if owner.activation == nil {
		if err := manager.events.waitOriginalEnd(); err != nil {
			return owner.retainUncertainAuxiliaryV1(errors.Join(stopErr, err))
		}
	} else {
		for {
			polls := []unix.PollFd{{Fd: int32(owner.activation.peer.pidfd), Events: unix.POLLIN}}
			count, err := unix.Poll(polls, -1)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
				return owner.retainUncertainAuxiliaryV1(errors.Join(stopErr, err))
			}
			break
		}
	}
	// Main exit does not discharge descendants. Require BOTH fixed native units
	// and BOTH native cgroup directories absent, while holding original OP+holder.
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := owner.vacancy.Check(ctx)
		cancel()
		if err == nil {
			break
		}
		if manager.checkNativeManager() != nil {
			return owner.retainUncertainAuxiliaryV1(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	owner.mu.Lock()
	owner.settled = true // Selected only after the fresh physical vacancy above.
	owner.mu.Unlock()
	ctx, cancel = context.WithTimeout(context.WithoutCancel(owner.ctx), 5*time.Second)
	err := withAuxiliaryPlacementV1(ctx, func(*placementControl) error { return owner.writeReservationV1(ctx, &owner.reservation, nil) }, false)
	cancel()
	if err != nil {
		return owner.retainUncertainAuxiliaryV1(err)
	}
	owner.mu.Lock()
	owner.published = false
	owner.settled = true
	owner.mu.Unlock()
	owner.publication.stopWatching()
	owner.once.Do(func() {
		if owner.activation != nil {
			owner.closeErr = errors.Join(owner.closeErr, owner.activation.Close())
		}
		owner.closeErr = errors.Join(owner.closeErr, owner.artifacts.Close(), owner.publication.close(), owner.vacancy.Close())
		owner.mu.Lock()
		owner.closed = true
		owner.mu.Unlock()
	})
	return owner.closeErr
}

// An irrecoverable original native source cannot be renewed from saved IDs.
// Retain this actual controller stack, leases and reservation indefinitely;
// it must never fall through a deferred original OP close or garbage collection.
func (owner *auxiliaryOperationV1) retainUncertainAuxiliaryV1(failure error) error {
	owner.closeErr = errors.Join(owner.closeErr, failure)
	select {}
}
