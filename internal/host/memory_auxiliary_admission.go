//go:build linux

package host

import (
	"context"
	"time"
)

// admitAuxiliaryMemoryLockedV1 is read-only accounting inside the SAME real
// placement transaction. It writes nothing and mints no capability. The real
// issuer must already hold whole-stopped/native vacancy and original artifact
// proof, and atomically publish proposed data before releasing that gate.
func admitAuxiliaryMemoryLockedV1(ctx context.Context, operation *SessionOperation, proposed *AuxiliaryReservationV1) (memoryRoomFactsV1, error) {
	if err := requireAuxiliaryAccountingOriginV1(ctx, operation); err != nil {
		return memoryRoomFactsV1{}, err
	}
	if operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || proposed == nil {
		return memoryRoomFactsV1{}, sessionLeaseError("auxiliary accounting has no original owning operation or complete proposal")
	}
	if err := validateAuxiliaryReservationV1Data(proposed); err != nil {
		return memoryRoomFactsV1{}, err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return memoryRoomFactsV1{}, err
	}
	if current.SandboxOf != "" || current.Wall == nil || current.Wall.Level != "microvm" || current.Auxiliary != nil || proposed.BootID != BootID() || time.Since(proposed.Since) > 2*time.Minute {
		return memoryRoomFactsV1{}, memoryAuxiliaryFailureV1("a new auxiliary reservation cannot replace another purpose or unsettled original leaf")
	}
	primary, err := readMemoryUnitV1(ctx, current.ID, true)
	if err != nil {
		return memoryRoomFactsV1{}, err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, current.ID)
	if err != nil {
		return memoryRoomFactsV1{}, err
	}
	if primary.exists || auxiliary.exists {
		return memoryRoomFactsV1{}, UserError("busy", "a native primary or auxiliary unit is still present", "Retain its genuine original native owner until actual closure before publishing this reservation.")
	}
	if err := checkNitroAdmissionLockedV1(ctx, operation, current); err != nil {
		return memoryRoomFactsV1{}, err
	}
	return memoryRoomLockedV1(ctx, operation, proposed.Declared)
}

// checkAuxiliaryMemoryStartLockedV1 rechecks DATA/native accounting immediately
// before the genuine issuer's actual native dispatch. The published auxiliary
// is already included in the scan: adding its Need again would count it twice.
func checkAuxiliaryMemoryStartLockedV1(ctx context.Context, operation *SessionOperation, expected *AuxiliaryReservationV1) error {
	if err := requireAuxiliaryAccountingOriginV1(ctx, operation); err != nil {
		return err
	}
	if operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || expected == nil {
		return sessionLeaseError("the auxiliary memory dispatch lost its original owning operation")
	}
	if err := validateAuxiliaryReservationV1Data(expected); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	actual := current.Auxiliary
	if actual == nil {
		return memoryAuxiliaryFailureV1("the auxiliary starting reservation disappeared")
	}
	if err := validateAuxiliaryReservationV1Data(actual); err != nil {
		return err
	}
	if actual.Version != expected.Version || actual.Token != expected.Token || actual.BootID != BootID() || actual.BootID != expected.BootID || actual.Since != expected.Since || actual.Declared != expected.Declared || time.Since(actual.Since) > 2*time.Minute {
		return memoryAuxiliaryFailureV1("the original auxiliary reservation expired or changed before native dispatch")
	}
	primary, err := readMemoryUnitV1(ctx, current.ID, true)
	if err != nil {
		return err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, current.ID)
	if err != nil {
		return err
	}
	if primary.exists || auxiliary.exists {
		return UserError("busy", "the pending auxiliary reservation already has a native unit", "Keep the same original controller and inspect native closure before another dispatch.")
	}
	facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
	if err != nil {
		return err
	}
	if !facts.Known || facts.Room < 0 {
		facts.Needed = actual.Declared
		return noMemoryRoomV1(facts)
	}
	return checkNitroAdmissionLockedV1(ctx, operation, current)
}
