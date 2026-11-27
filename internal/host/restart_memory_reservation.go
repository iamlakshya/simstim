package host

import (
	"context"
	"errors"
	"time"
)

func restartMemoryAllowsDrainV1(operation *SessionOperation) bool {
	operation.gate.Lock()
	defer operation.gate.Unlock()
	receipt := operation.restartMemory
	return operation.active.Load() && receipt != nil && receipt.operation == operation && !receipt.used && receipt.restore
}

// reserveRestartMemoryLockedV1 holds the SAME kernel holder-lock role until
// this writer finishes. It gives no stop/boot grant. The old unit must be
// absent before its reservation can be replaced, even for a dead socket.
func reserveRestartMemoryLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest) (*sessionFileLease, error) {
	if err := requireNoAuxiliaryMemoryLockedV1(ctx, operation, next); err != nil {
		return nil, err
	}
	if !isWalledMemoryV1(next) {
		return nil, nil
	}
	if next.Limits.Declared != next.Limits.MemoryMax {
		return nil, sessionLeaseError("the restart understates its complete unit reservation")
	}
	if err := checkExactWalledCapV1(next.Limits.MemoryMax); err != nil {
		return nil, err
	}
	retained, err := checkColdRestartReservationLockedV1(ctx, operation, next)
	if err != nil {
		return nil, err
	}
	var vacant *sessionFileLease
	if !retained {
		vacant, err = openOwnedSessionFileLease(ctx, operation.Primary(), holderLockFile, 0)
		if err != nil {
			return nil, err
		}
		if err := operation.native.rootRef.CheckRoot(vacant.root); err != nil {
			return nil, errors.Join(err, vacant.Close())
		}
	}
	fail := func(cause error) (*sessionFileLease, error) { return nil, errors.Join(cause, vacant.Close()) }
	unit, err := readMemoryUnitV1(ctx, operation.Primary(), true)
	if err != nil {
		return fail(attachMemoryFactsV1(err, next.Limits.Declared))
	}
	if unit.exists {
		return fail(UserError("busy", "the old canonical sandbox unit still owns its reservation", "Keep the original owner stopped and wait for its native unit to finish before retrying."))
	}
	if _, err := memoryRoomLockedV1(ctx, operation, next.Limits.Declared); err != nil {
		return fail(err)
	}
	if err := checkNitroAdmissionLockedV1(ctx, operation, next); err != nil {
		return fail(err)
	}
	next.Run.BootID = BootID()
	next.Status.State, next.Status.Since = StateStarting, time.Now().UTC()
	return vacant, nil
}

// Restore skips the read-only step2 drain precheck. A valid old root identity
// still grants no metadata publication while purge owns that root scope.
func checkRestartMemoryPublicationV1(operation *SessionOperation, control *placementControl) error {
	if restartMemoryAllowsDrainV1(operation) && (control.Drain == "purge" || control.Destructive) {
		return UserError("draining", "purge owns this root generation; restore cannot publish a new reservation", "Finish the original removal before restoring this host.")
	}
	return nil
}
