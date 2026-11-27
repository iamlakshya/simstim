package host

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// AuxUnitNameV1 is a closed canonical NAME projection, not native authority.
// Only the genuine auxiliary producer may create/use this unit with its held
// original primary OP, stopped/native proof and original artifact capture.
func AuxUnitNameV1(primary string) (string, error) {
	if !validID(primary) {
		return "", sessionLeaseError("the auxiliary unit has no original canonical primary ID")
	}
	return brand.UnitPrefix + primary + "-aux.service", nil
}

func auxiliaryMemoryKeyV1(primary string) string { return primary + "-aux" }

func canonicalMemoryUnitKeyV1(unit string) (string, error) {
	if !strings.HasPrefix(unit, brand.UnitPrefix) || !strings.HasSuffix(unit, ".service") {
		return "", memoryAuxiliaryFailureV1("the branded native unit has an unsupported closed role")
	}
	id := strings.TrimSuffix(strings.TrimPrefix(unit, brand.UnitPrefix), ".service")
	if strings.HasSuffix(id, "-aux") {
		primary := strings.TrimSuffix(id, "-aux")
		expected, err := AuxUnitNameV1(primary)
		if err != nil || expected != unit {
			return "", memoryAuxiliaryFailureV1("the native auxiliary identity is not canonical")
		}
		return auxiliaryMemoryKeyV1(primary), nil
	}
	if !validID(id) || UnitName(id) != unit {
		return "", memoryAuxiliaryFailureV1("the native primary identity is not canonical")
	}
	return id, nil
}

// auxiliaryReservationChargeV1 observes only accounting data/native facts.
// In particular its token, active result and unit absence are NEVER a stopped,
// namespace, original boot, artifact or deletion capability.
func auxiliaryReservationChargeV1(m *Manifest, boot string, now time.Time, primary, aux memoryUnitFactsV1) (int64, bool, error) {
	if m == nil {
		return 0, false, memoryAuxiliaryFailureV1("the protected primary record is unavailable")
	}
	r := m.Auxiliary
	if r == nil {
		if aux.exists {
			return 0, false, memoryAuxiliaryFailureV1("a native auxiliary has no protected reservation")
		}
		return 0, false, nil
	}
	if m.SandboxOf != "" || m.Wall == nil || m.Wall.Level != "microvm" {
		return 0, false, memoryAuxiliaryFailureV1("an auxiliary reservation belongs to another protected primary purpose")
	}
	if err := validateAuxiliaryReservationV1Data(r); err != nil {
		return 0, false, errors.Join(memoryAuxiliaryFailureV1("the auxiliary accounting version or data is unknown"), err)
	}
	starting := r.BootID == boot && now.Sub(r.Since) <= 2*time.Minute
	if aux.exists {
		if r.BootID != boot || aux.maximum != r.Declared || aux.swap != 0 {
			return 0, false, memoryAuxiliaryFailureV1("the native auxiliary cap or boot differs from its complete reservation")
		}
		starting = true
	}
	if !starting {
		return 0, false, nil
	}
	if primary.exists {
		return 0, false, memoryAuxiliaryFailureV1("ordinary and auxiliary native units overlap")
	}
	// One protected reservation replaces any old no-unit ordinary starting
	// charge while the auxiliary is active. Run/Life remain unchanged.
	return r.Declared, true, nil
}

func memoryAuxiliaryFailureV1(message string) error {
	return UserError("no_room", message, "Retain the original auxiliary owner and its native resources; inspect its protected reservation before admitting another sandbox.")
}

func requireNoAuxiliaryMemoryLockedV1(ctx context.Context, operation *SessionOperation, current *Manifest) error {
	if operation == nil || current == nil || current.ID != operation.Primary() || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("ordinary memory admission has no original primary operation")
	}
	if current.Auxiliary != nil {
		return UserError("busy", "an uncleared auxiliary reservation owns this primary", "Finish the original auxiliary operation and its actual native lifetime before an ordinary start.")
	}
	if !isWalledMemoryV1(current) {
		return nil
	}
	unit, err := readAuxMemoryUnitV1(ctx, current.ID)
	if err != nil {
		return attachMemoryFactsV1(err, current.Limits.MemoryMax)
	}
	if unit.exists {
		return UserError("busy", "a native auxiliary unit still owns this primary", "Retain and finish its original native auxiliary owner before starting the primary.")
	}
	return nil
}
