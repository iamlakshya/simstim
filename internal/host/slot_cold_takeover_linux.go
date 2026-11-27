//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"golang.org/x/sys/unix"
)

// This actual attempt retains the fresh current Warm and Source constructors,
// not their public request data. It is created only for this original Cold.
type slotColdTakeoverV2 struct {
	issuer           *slotColdTakeoverV2
	origin           *SlotSnapshotOwnerV1
	cold             *ColdResumeV1
	launch           *startupLaunchV1
	warm             *WarmResumeV1
	unexpected       *ColdResumeV1
	source           *SlotSnapshotOwnerV1
	creation         *os.File
	oldLease         *os.File
	verified, closed bool
	closeErr         error
}

// ValidateNewColdSourceV2 observes the SAME actual new native creation after
// ready. Ready is only the point at which its borrowed startup OP has closed;
// this new native Source and SAME-OFD observation establish the real handoff.
func (owner *SlotSnapshotOwnerV1) ValidateNewColdSourceV2(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return err
	}
	defer done()
	if cold == nil {
		return rewindFailureV1("the reserved takeover has no original Cold owner")
	}
	cold.life.Lock()
	defer cold.life.Unlock()
	cold.mu.Lock()
	launch := cold.startup
	valid := owner.cold == cold && operation == owner.operation && cold.operation == operation && cold.bound && cold.handed && !cold.closed && cold.creation != nil && cold.vacancy != nil && launch != nil && launch.operation == operation && launch.unit != nil
	cold.mu.Unlock()
	if !valid {
		return rewindFailureV1("the new slot Source has another original Cold creation or operation")
	}
	launch.mu.Lock()
	ready := launch.ready
	launch.mu.Unlock()
	if !ready {
		return rewindFailureV1("the original new creation has not reached its committed source observation")
	}
	owner.operations.Lock()
	defer owner.operations.Unlock()
	if err := owner.checkColdRetrySourceV2(ctx, cold); err != nil {
		return err
	}
	cold.mu.Lock()
	same := cold.startup == launch && cold.creation != nil && !cold.closed && cold.artifacts != nil && cold.artifacts.slot != nil && cold.artifacts.slot.owner == owner && len(cold.artifacts.slot.roles) == 18
	cold.mu.Unlock()
	if !same {
		return rewindFailureV1("the actual reserved artifacts changed before their new Source observation")
	}
	attempt := owner.takeover
	if attempt == nil {
		attempt = &slotColdTakeoverV2{origin: owner, cold: cold, launch: launch, oldLease: cold.artifacts.slot.roles[17]}
		attempt.issuer = attempt
		owner.takeover = attempt
		attempt.creation, err = duplicateSlotSnapshotFileV1(cold.creation)
		if err != nil {
			return err
		}
	}
	if attempt.issuer != attempt || attempt.origin != owner || attempt.cold != cold || attempt.launch != launch || attempt.closed {
		return rewindFailureV1("the original slot takeover attempt ended or was substituted")
	}
	if attempt.warm == nil {
		attempt.warm, attempt.unexpected, err = InspectResumeForOperationV1(ctx, operation)
		if err != nil {
			return err
		}
		if attempt.warm == nil || attempt.unexpected != nil {
			return rewindFailureV1("the original new slot creation has no actual current Warm source")
		}
	}
	if attempt.source == nil {
		var local bool
		attempt.source, local, err = attempt.warm.captureSlotSnapshotReservedV2(ctx, operation)
		if err != nil {
			return err
		}
		if local || attempt.source == nil {
			return rewindFailureV1("the new native creation did not supply its actual reserved slot Source")
		}
	}
	if err := attempt.checkV2(ctx); err != nil {
		return err
	}
	attempt.verified = true
	return nil
}

func (attempt *slotColdTakeoverV2) checkV2(ctx context.Context) error {
	if attempt == nil || attempt.issuer != attempt || attempt.origin == nil || attempt.origin.takeover != attempt || attempt.cold == nil || attempt.launch == nil || attempt.launch.unit == nil || attempt.warm == nil || attempt.source == nil || attempt.creation == nil || attempt.closed || !attempt.source.admitted || attempt.source.reserved == nil || attempt.source.warm != attempt.warm {
		return rewindFailureV1("the actual slot takeover has incomplete native custody")
	}
	source := attempt.source
	if source == attempt.origin || source.operation != attempt.origin.operation || source.warm.owner == attempt.origin.warm.owner {
		return rewindFailureV1("the old slot Source cannot stand in for its new native creation")
	}
	first, err := slotSnapshotPIDV1(attempt.creation, true)
	if err != nil {
		return err
	}
	current, err := slotSnapshotPIDV1(source.supervisor, true)
	if err != nil || current != first {
		return errors.Join(rewindFailureV1("the new slot Source belongs to another original supervisor birth"), err)
	}
	invocation := source.invocation
	original := attempt.launch.unit
	if invocation == nil || invocation.path != original.path || invocation.unit != original.unit || !sameUnitActivationProperties(invocation.properties, original.properties) || !os.SameFile(invocation.executable, original.executable) {
		return rewindFailureV1("the new slot Source has another original native invocation or code")
	}
	if !validOriginalReadyNeed(attempt.launch.manifest, &source.warm.owner.source) {
		return rewindFailureV1("the new slot Source changed the admitted original boot selection")
	}
	if err := errors.Join(source.checkRunningV1(ctx), checkSameSlotLeaseOFDV2(attempt.oldLease, source.lease)); err != nil {
		return err
	}
	last, err := slotSnapshotPIDV1(attempt.creation, true)
	if err != nil || last != first {
		return errors.Join(rewindFailureV1("the original new supervisor ended during slot handoff"), err)
	}
	return ctx.Err()
}

func checkSameSlotLeaseOFDV2(original, current *os.File) error {
	if original == nil || current == nil {
		return rewindFailureV1("the slot handoff has no two actual reservation descriptions")
	}
	// Observe each actual locked OFD. Locker PID is not sender identity.
	if err := errors.Join(slotReservationObservationV2(original), slotReservationObservationV2(current)); err != nil {
		return err
	}
	pid := uintptr(os.Getpid())
	order, _, errno := unix.Syscall6(unix.SYS_KCMP, pid, pid, 0, original.Fd(), current.Fd(), 0)
	if errno != 0 {
		return errors.Join(rewindFailureV1("the original slot lease open descriptions could not be compared"), errno)
	}
	if order != 0 {
		return rewindFailureV1("the new keeper does not retain the SAME original slot lease open description")
	}
	return nil
}

func slotReservationObservationV2(file *os.File) error {
	// This observation grants no Source or acquisition right. The two actual
	// closed constructors above still provide creation/namespace/OP authority.
	return slotlaunch.CheckOriginalReservationLeaseV2(file)
}
