//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"reflect"

	"golang.org/x/sys/unix"
)

// checkPurgeAuxiliarySourceV1 is the concrete native half of the original
// purge owner's control predicate. No supplied checker, saved process number,
// status flag or reservation can replace these original retained objects.
func (stopped *StoppedSessionV1) checkPurgeAuxiliarySourceV1(ctx context.Context, operation *SessionOperation, auxiliary *auxiliaryOperationV1) error {
	if auxiliary == nil {
		return stopped.Check(ctx, operation)
	}
	if stopped == nil || ctx == nil || operation == nil {
		return auxiliaryUnavailableV1("the purge reader lost its original physical stopped source")
	}
	stopped.mu.Lock()
	defer stopped.mu.Unlock()
	if stopped.closed || !stopped.settled || stopped.operation != operation || stopped.holder == nil || stopped.invocation == nil || stopped.invocation.peer == nil || stopped.origin == nil || stopped.manager == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || BootID() != stopped.boot {
		return auxiliaryUnavailableV1("the purge reader has another or ended original stopped source")
	}
	if auxiliary.operation != operation || auxiliary.vacancy == nil || auxiliary.vacancy.stopped != stopped || auxiliary.vacancy.operation != operation || auxiliary.vacancy.holder == nil || auxiliary.vacancy.manager == nil || auxiliary.artifacts == nil || auxiliary.artifacts.vacancy != auxiliary.vacancy || auxiliary.artifacts.placement == nil {
		return auxiliaryUnavailableV1("the purge reader has no actual matching auxiliary issuer")
	}
	if err := errors.Join(CheckCaller(), stopped.origin.checkIdentity(), stopped.manager.checkNativeManager(), stopped.holder.check(ctx), operation.native.rootRef.CheckRoot(stopped.holder.root), auxiliary.vacancy.holder.check(ctx), operation.native.rootRef.CheckRoot(auxiliary.vacancy.holder.root), auxiliary.vacancy.manager.checkNativeManager(), auxiliary.artifacts.placement.checkIdentity(), ctx.Err()); err != nil {
		return err
	}
	if auxiliary.artifacts.domain != stopped.origin.domain() || auxiliary.vacancy.boot != stopped.boot || !bytes.Equal(auxiliary.vacancy.need, stopped.source.VMNeedV1) {
		return auxiliaryUnavailableV1("the actual auxiliary placement or saved source differs from the original stop")
	}
	polls := []unix.PollFd{{Fd: int32(stopped.invocation.peer.pidfd), Events: unix.POLLIN}}
	count, err := unix.Poll(polls, 0)
	if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
		return errors.Join(auxiliaryUnavailableV1("the original supervisor has no positive native exit"), err)
	}
	empty, err := originalStopCgroupEmptyV1(stopped.invocation.cgroup)
	if err != nil || !empty {
		return errors.Join(auxiliaryUnavailableV1("the original supervisor cgroup is not physically empty"), err)
	}
	if err := stopped.manager.requireAuxiliaryUnitAbsentV1(ctx, stopped.invocation.unit); err != nil {
		return err
	}
	primary, err := readMemoryUnitV1(ctx, operation.Primary(), true)
	if err != nil || primary.exists {
		return errors.Join(auxiliaryUnavailableV1("the original primary native cgroup is not absent"), err)
	}
	auxiliary.mu.Lock()
	dispatched, settled, closed, published := auxiliary.dispatched, auxiliary.settled, auxiliary.closed, auxiliary.published
	activation, creation, job := auxiliary.activation, auxiliary.creation, auxiliary.job
	auxiliary.mu.Unlock()
	if closed {
		return auxiliaryUnavailableV1("the original auxiliary owner already ended")
	}
	if !dispatched && (activation != nil || creation != nil || job != "") {
		return auxiliaryUnavailableV1("the original auxiliary pre-dispatch state has a creation counterpart")
	}
	fresh, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(fresh)
	if err != nil {
		return err
	}
	var reservation *AuxiliaryReservationV1
	if published {
		reservation = &auxiliary.reservation
	}
	if fresh.ID != operation.Primary() || fresh.SandboxOf != "" || !bytes.Equal(fresh.VMNeedV1, stopped.source.VMNeedV1) || !bytes.Equal(runtime, auxiliary.vacancy.runtime) || !reflect.DeepEqual(fresh.RuntimeManifest, stopped.source.RuntimeManifest) || !reflect.DeepEqual(fresh.Auxiliary, reservation) {
		return auxiliaryUnavailableV1("the original purge auxiliary source or reservation changed")
	}
	if !dispatched || settled {
		// The flags select this path only. Fresh physical vacancy is mandatory
		// even after Close observed exit, and original creation exit is retained.
		if err := auxiliary.vacancy.Check(ctx); err != nil {
			return err
		}
		if dispatched {
			if activation != nil && activation.peer != nil {
				polls := []unix.PollFd{{Fd: int32(activation.peer.pidfd), Events: unix.POLLIN}}
				count, err := unix.Poll(polls, 0)
				if err != nil || count != 1 || polls[0].Revents&unix.POLLIN == 0 {
					return errors.Join(auxiliaryUnavailableV1("the original auxiliary creation has no positive native exit"), err)
				}
			} else {
				events := &auxiliary.vacancy.manager.events
				events.mu.Lock()
				ended := events.negativeReply || events.unitRemoved && events.observedJob != "" && events.observedJob == events.removedJob && events.jobID == events.removedID
				events.mu.Unlock()
				if !ended {
					return auxiliaryUnavailableV1("the original auxiliary creation has no authenticated negative or removal result")
				}
			}
		}
	} else if activation != nil {
		if err := auxiliary.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
			return err
		}
	} else {
		// The exact original native creation request remains retained while its
		// first activation is being resolved. It authorises no private loan;
		// that still waits for actual creation pidfd/code/cgroup/cap proof.
		if creation == nil || creation.Destination != "org.freedesktop.systemd1" || creation.Path != systemdManagerPath || creation.Method != systemdManagerInterface+".StartTransientUnit" || !auxiliary.vacancy.manager.events.wasDispatched() {
			return auxiliaryUnavailableV1("the pending auxiliary has no retained original native creation request")
		}
		if err := auxiliary.vacancy.manager.events.failure(); err != nil {
			return err
		}
	}
	return errors.Join(stopped.manager.requireAuxiliaryUnitAbsentV1(ctx, stopped.invocation.unit), stopped.manager.checkNativeManager(), auxiliary.vacancy.manager.checkNativeManager(), ctx.Err())
}
