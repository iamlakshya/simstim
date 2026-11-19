package host

import (
	"context"
	"time"
)

// The actual native send/start calls this inside its existing short placement
// gate. A reservation that expired without a unit cannot later launch against
// room that another process has since reserved.
func checkStartingMemoryLockedV1(ctx context.Context, operation *SessionOperation, expected *Manifest) error {
	if !isWalledMemoryV1(expected) {
		return nil
	}
	if operation == nil {
		return sessionLeaseError("native memory start has no original owning operation")
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if current.Status.State != StateStarting || current.Run.BootID != BootID() || current.Status.Since.IsZero() || time.Since(current.Status.Since) > 2*time.Minute ||
		current.Limits.Declared != current.Limits.MemoryMax || current.Limits.MemoryMax != expected.Limits.MemoryMax || current.Limits.Declared != expected.Limits.Declared {
		return attachMemoryFactsV1(UserError("no_room", "the original starting memory reservation expired or changed before native dispatch", "Prepare a fresh start through the original session operation."), current.Limits.MemoryMax)
	}
	unit, err := readMemoryUnitV1(ctx, current.ID, true)
	if err != nil {
		return attachMemoryFactsV1(err, current.Limits.Declared)
	}
	if unit.exists {
		return sessionLeaseError("the pending reservation already has another native unit")
	}
	_, err = memoryRoomLockedV1(ctx, operation, current.Limits.Declared)
	return err
}
