//go:build linux

package host

import (
	"context"
	"reflect"
	"time"
)

func resumePreparationForOperationV1(operation *SessionOperation) *resumePreparationLeaseV1 {
	if operation == nil {
		return nil
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	return operation.resumePreparation
}

// reserveRestartMemoryControlLockedV1 is called by the SAME actual restart
// writer with its current control. Ordinary starts keep their exact old path.
func reserveRestartMemoryControlLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest, control *placementControl) (*sessionFileLease, error) {
	if handled, err := reserveManagedNoneRestartRSSLockedV1(ctx, operation, next, control); handled {
		return nil, err
	}
	if next == nil || next.ResumePreparation == nil {
		return reserveRestartMemoryLockedV1(ctx, operation, next)
	}
	lease := resumePreparationForOperationV1(operation)
	if lease == nil || control == nil || operation.nativeColdResume != lease.cold {
		return nil, resumePreparationErrorV1("the restart cannot adopt a preparation data record")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.transferred {
		return nil, resumePreparationErrorV1("the original preparation has not transferred to startup")
	}
	if _, err := lease.checkCurrentLockedV1(ctx, operation, lease.cold, control); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(next.ResumePreparation, &lease.value) || next.Limits.MemoryMax != lease.value.MemoryMax || next.Limits.Declared != lease.value.MemoryMax {
		return nil, resumePreparationDataErrorV1()
	}
	if err := lease.cold.CheckManifest(ctx, operation, next); err != nil {
		return nil, err
	}
	if err := checkUserDrainLockedV1(ctx, control); err != nil {
		return nil, err
	}
	unit, err := readMemoryUnitV1(ctx, operation.Primary(), true)
	if err != nil {
		return nil, err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, operation.Primary())
	if err != nil {
		return nil, err
	}
	if auxiliary.exists {
		return nil, resumePreparationErrorV1("the original pending native unit is no longer absent")
	}
	if unit.exists {
		if lease.cold.custody == nil {
			return nil, resumePreparationErrorV1("the original pending native unit is no longer absent")
		}
		if err := lease.cold.custody.checkBootstrapUnitV1(ctx, unit); err != nil {
			return nil, err
		}
	}
	facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	if facts.Room < 0 {
		facts.Needed = lease.value.MemoryMax
		return nil, noMemoryRoomV1(facts)
	}
	// The genuine Cold source keeps the actual vacancy holder. Do not reopen
	// it, and do not add the already-counted complete quantum a second time.
	next.Run.BootID = BootID()
	next.Status.State, next.Status.Since = StateStarting, time.Now().UTC()
	return nil, nil
}

func checkResumePreparationStartLockedV1(ctx context.Context, operation *SessionOperation, expected *Manifest, control *placementControl) error {
	lease := resumePreparationForOperationV1(operation)
	if lease == nil || control == nil || operation.nativeColdResume != lease.cold {
		return resumePreparationErrorV1("native dispatch has no original held preparation")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if !lease.transferred {
		return resumePreparationErrorV1("native dispatch cannot consume an untransferred preparation")
	}
	current, err := lease.checkCurrentLockedV1(ctx, operation, lease.cold, control)
	if err != nil {
		return err
	}
	if expected == nil || current.Status.State != StateStarting || current.Run.BootID != BootID() || current.Status.Since.IsZero() || current.Limits != expected.Limits || !reflect.DeepEqual(expected.ResumePreparation, &lease.value) {
		return resumePreparationErrorV1("the original Starting projection changed before dispatch")
	}
	if err := checkUserDrainLockedV1(ctx, control); err != nil {
		return err
	}
	unit, err := readMemoryUnitV1(ctx, operation.Primary(), true)
	if err != nil {
		return err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, operation.Primary())
	if err != nil {
		return err
	}
	if auxiliary.exists {
		return resumePreparationErrorV1("native dispatch found another ordinary or auxiliary unit")
	}
	if unit.exists {
		if lease.cold.custody == nil {
			return resumePreparationErrorV1("native dispatch found another ordinary or auxiliary unit")
		}
		if err := lease.cold.custody.checkBootstrapUnitV1(ctx, unit); err != nil {
			return err
		}
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
}
