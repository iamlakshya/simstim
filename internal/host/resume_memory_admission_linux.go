//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"time"
)

// resumePreparationLeaseV1 exists only on the SAME original OP and the real
// bridge/native cold producer. The persisted leaf is correlation/accounting
// data. Neither a DTO, nonce, context flag nor a capacity observation mints it.
type resumePreparationLeaseV1 struct {
	mu                                           sync.Mutex
	origin                                       *resumePreparationAccountingOriginV1
	operation                                    *SessionOperation
	cold                                         *ColdResumeV1
	publication                                  *runtimePublication
	value                                        ResumePreparationV1
	need, runtime                                []byte
	selection                                    *resumePreparationSelectionV1
	published, transferred, takenOver, cancelled bool
}

func beginResumePreparationV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, preview *Manifest, nonce string) (_ *resumePreparationLeaseV1, failure error) {
	if cold != nil && cold.custody != nil {
		return cold.custody.adoptBootstrapPreparationV1(ctx, operation, cold, preview, nonce)
	}
	origin, err := requireResumePreparationAccountingOriginV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := origin.check(ctx, operation, cold); err != nil {
		return nil, err
	}
	publication := origin.publicationV1()
	lifetime := origin.lifetimeV1()
	if preview == nil || publication == nil || lifetime == nil || lifetime.Err() != nil || nonce != origin.nonceV1() || preview.ID != operation.Primary() || preview.SandboxOf != "" || preview.path != "" || !isWalledMemoryV1(preview) || preview.Limits.Declared != preview.Limits.MemoryMax {
		return nil, resumePreparationDataErrorV1()
	}
	if err := cold.CheckManifest(ctx, operation, preview); err != nil {
		return nil, err
	}
	selected, err := startupRuntimeBytes(preview)
	if err != nil {
		return nil, err
	}
	selection, err := captureResumePreparationSelectionV1(ctx, operation, preview)
	if err != nil {
		return nil, err
	}
	lease := &resumePreparationLeaseV1{selection: selection, origin: origin, operation: operation, cold: cold, publication: publication, need: bytes.Clone(preview.VMNeedV1), runtime: bytes.Clone(selected), value: ResumePreparationV1{V: 1, Nonce: nonce, Boot: BootID(), Since: time.Now().UTC(), MemoryMax: preview.Limits.Declared}}
	if selection.before.MemoryMax != selection.target.MemoryMax {
		lease.value.PreviousMemoryMax = selection.before.MemoryMax
	}
	if err := validateResumePreparationV1Data(&lease.value); err != nil {
		return nil, err
	}
	operation.gate.Lock()
	if !operation.active.Load() || operation.resumePreparation != nil || operation.startup != nil {
		operation.gate.Unlock()
		return nil, resumePreparationErrorV1("the original preparation was already reserved or started")
	}
	operation.resumePreparation = lease
	operation.gate.Unlock()
	// Return the SAME owner on every later error. A write/sync error may have
	// published this exact leaf; caller cleanup must retain and settle it.
	err = publication.gateControl(ctx, func(control *placementControl) error {
		if err := lease.checkOriginLockedV1(ctx, operation, cold, control); err != nil {
			return err
		}
		if err := checkUserDrainLockedV1(ctx, control); err != nil {
			return err
		}
		current, err := operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if current.ResumePreparation != nil || current.Auxiliary != nil {
			return resumePreparationErrorV1("the pending complete cap changed or another preparation owns this primary")
		}
		if err := lease.selection.checkCurrentV1(operation, current); err != nil {
			return err
		}
		unit, err := readMemoryUnitV1(ctx, current.ID, true)
		if err != nil {
			return err
		}
		auxiliary, err := readAuxMemoryUnitV1(ctx, current.ID)
		if err != nil {
			return err
		}
		if unit.exists || auxiliary.exists {
			return resumePreparationErrorV1("native absence changed before the preparation reservation")
		}
		if _, err := memoryRoomLockedV1(ctx, operation, lease.value.MemoryMax); err != nil {
			return err
		}
		if err := checkNitroAdmissionLockedV1(ctx, operation, preview); err != nil {
			return err
		}
		return lease.writeLeafLockedV1(ctx, nil, &lease.value)
	})
	return lease, err
}

func (lease *resumePreparationLeaseV1) checkOriginLockedV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, control *placementControl) error {
	if lease == nil || ctx == nil || control == nil || lease.origin == nil || lease.publication == nil || operation == nil || operation != lease.operation || cold == nil || cold != lease.cold {
		return resumePreparationErrorV1("the original memory preparation source is absent or changed")
	}
	if err := lease.origin.check(ctx, operation, cold); err != nil {
		return err
	}
	publication := lease.publication
	if lease.origin.publicationV1() != publication || publication.startup != operation || publication.operation == nil || publication.owner == nil || publication.operation.owner != publication.owner || publication.operation.record.Kind != lease.origin.kindV1() || publication.owner.installer || lease.origin.nonceV1() != lease.value.Nonce {
		return resumePreparationErrorV1("the memory preparation lost its original native publication")
	}
	if current, ok := control.Active[publication.operation.token]; !ok || current != publication.operation.record {
		return resumePreparationErrorV1("the actual preparation active record changed")
	}
	operation.gate.Lock()
	same := operation.active.Load() && operation.resumePreparation == lease
	operation.gate.Unlock()
	if !same {
		return resumePreparationErrorV1("the original OP no longer holds this preparation")
	}
	return errors.Join(publication.owner.checkIdentity(), operation.native.check(ctx), ctx.Err())
}
func (lease *resumePreparationLeaseV1) checkCurrentLockedV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, control *placementControl) (*Manifest, error) {
	if lease.cancelled || lease.takenOver {
		return nil, resumePreparationErrorV1("the original pending preparation has already settled")
	}
	if err := lease.checkOriginLockedV1(ctx, operation, cold, control); err != nil {
		return nil, err
	}
	lifetime := lease.origin.lifetimeV1()
	if lifetime == nil || lifetime.Err() != nil {
		return nil, resumePreparationErrorV1("the original preparation request lifetime ended")
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current.ResumePreparation, &lease.value) || current.Auxiliary != nil {
		return nil, resumePreparationErrorV1("the protected pending preparation or complete cap changed")
	}
	if err := lease.selection.checkCurrentV1(operation, current); err != nil {
		return nil, err
	}
	return current, nil
}
func (lease *resumePreparationLeaseV1) Check(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	if lease == nil {
		return resumePreparationDataErrorV1()
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.takenOver {
		return lease.checkSettledSourceV1(ctx, operation, cold)
	}
	return lease.publication.gateControl(ctx, func(control *placementControl) error {
		if _, err := lease.checkCurrentLockedV1(ctx, operation, cold, control); err != nil {
			return err
		}
		facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
		if err != nil {
			return err
		}
		if facts.Room < 0 {
			facts.Needed = lease.value.MemoryMax
			return noMemoryRoomV1(facts)
		}
		return nil
	})
}
func (lease *resumePreparationLeaseV1) checkSettledSourceV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	if ctx == nil || operation == nil || operation != lease.operation || cold != lease.cold || !operation.Valid(operation.Primary()) || lease.cancelled {
		return resumePreparationDataErrorV1()
	}
	return ctx.Err()
}

// TransferToStarting preserves the SAME leaf/charge. The actual restart
// writer refreshes Since under placement after long copy; no two-minute gap
// or clear-and-re-admit transition is introduced here.
func (lease *resumePreparationLeaseV1) TransferToStarting(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, next *Manifest) error {
	if lease == nil || next == nil {
		return resumePreparationDataErrorV1()
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.transferred || lease.takenOver || lease.cancelled {
		return resumePreparationErrorV1("the original preparation transfer was already consumed")
	}
	if next.ID != operation.Primary() || next.Limits.MemoryMax != lease.value.MemoryMax || next.Limits.Declared != lease.value.MemoryMax {
		return resumePreparationDataErrorV1()
	}
	if err := lease.selection.checkTargetV1(operation, next); err != nil {
		return err
	}
	if err := cold.CheckManifest(ctx, operation, next); err != nil {
		return err
	}
	err := lease.publication.gateControl(ctx, func(control *placementControl) error {
		if _, err := lease.checkCurrentLockedV1(ctx, operation, cold, control); err != nil {
			return err
		}
		if err := checkUserDrainLockedV1(ctx, control); err != nil {
			return err
		}
		facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
		if err != nil {
			return err
		}
		if facts.Room < 0 {
			facts.Needed = lease.value.MemoryMax
			return noMemoryRoomV1(facts)
		}
		next.ResumePreparation = cloneResumePreparationV1(&lease.value)
		return nil
	})
	if err == nil {
		lease.transferred = true
	}
	return err
}

// Cancel operates after the real producer joined its workers. It first asks
// the SAME Cold source to cancel/join any actual new startup and prove whole
// absence. Independent cleanup context is permitted; cancelled worker context
// alone never releases a reservation. Purge can cancel work but cannot bypass
// this exact private native cleanup/Active-record check.
func (lease *resumePreparationLeaseV1) Cancel(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	if lease == nil {
		return resumePreparationDataErrorV1()
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if operation == nil || cold == nil || lease.origin == nil || lease.publication == nil || operation != lease.operation || cold != lease.cold || ctx == nil {
		return resumePreparationDataErrorV1()
	}
	if lease.takenOver || lease.cancelled {
		return nil
	}
	if err := cold.SettleForCancellationV1(ctx, operation); err != nil {
		return err
	}
	if err := lease.origin.check(ctx, operation, cold); err != nil {
		return err
	}
	// This is the closed cleanup purpose, not a start/publish drain exception.
	// It can remove only this original leaf after actual native cleanup.
	return withPlacementRoot(ctx, true, func(control *placementControl, _ *os.Root) error {
		if err := lease.checkOriginLockedV1(ctx, operation, cold, control); err != nil {
			return err
		}
		current, err := operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if current.ResumePreparation == nil && !lease.published {
			lease.cancelled = true
			return nil
		}
		if !reflect.DeepEqual(current.ResumePreparation, &lease.value) {
			return resumePreparationErrorV1("cleanup cannot clear another preparation")
		}
		if err := lease.writeLeafLockedV1(ctx, &lease.value, nil); err != nil {
			return err
		}
		lease.cancelled = true
		return nil
	}, false)
}

// The actual native sender calls this INSIDE its gate, after real new code,
// creation/current-sender, complete cap/swap/membership and disk checks, before
// any private OP loan. No extra lock or Starting/ACK interpretation occurs.
func takeoverResumePreparationNativeLockedV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, control *placementControl) error {
	if operation == nil {
		return resumePreparationDataErrorV1()
	}
	operation.gate.Lock()
	lease := operation.resumePreparation
	operation.gate.Unlock()
	if lease == nil {
		return resumePreparationErrorV1("native takeover has no original held preparation")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.transferred || lease.takenOver || lease.cancelled || operation.nativeColdResume != cold {
		return resumePreparationErrorV1("native takeover changed its actual original source")
	}
	current, err := lease.checkCurrentLockedV1(ctx, operation, cold, control)
	if err != nil {
		return err
	}
	launch := operation.startup
	if launch == nil || launch.operation != operation || launch.placement == nil || launch.placement.startup != operation || launch.unit == nil || launch.nativePeer() == nil || launch.nativePeer().identity == nil {
		return resumePreparationErrorV1("native takeover lacks the actual new startup creation owner")
	}
	if active, ok := control.Active[launch.placement.operation.token]; !ok || active != launch.placement.operation.record {
		return resumePreparationErrorV1("the actual new startup active record changed")
	}
	if err := checkColdNativeTakeoverLockedV1(ctx, operation); err != nil {
		return err
	}
	unit, err := readMemoryUnitV1(ctx, operation.Primary(), true)
	if err != nil {
		return err
	}
	if !unit.exists || unit.maximum != lease.value.MemoryMax || unit.swap != 0 || current.Run.BootID != BootID() || current.Limits.Declared != lease.value.MemoryMax {
		return resumePreparationErrorV1("the actual native unit cannot continuously take over this complete charge")
	}
	if err := lease.writeLeafLockedV1(ctx, &lease.value, nil); err != nil {
		return err
	}
	lease.takenOver = true
	return nil
}
