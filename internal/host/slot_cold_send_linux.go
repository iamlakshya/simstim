//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// WithReservedStartupFilesV2 runs only for the actual new startup sender,
// with launch.mu already held. The separate physical writer adds its SAME OP
// as role0; these eighteen actual native operands never widen the OP1 reader.
func (owner *SlotSnapshotOwnerV1) WithReservedStartupFilesV2(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, launch *startupLaunchV1, send func([]*os.File) error) error {
	done, err := owner.beginV1(ctx)
	if err != nil {
		return err
	}
	defer done()
	if cold == nil || launch == nil || send == nil {
		return rewindFailureV1("the reserved startup loan has no actual creator and sender")
	}
	owner.operations.Lock()
	defer owner.operations.Unlock()
	cold.mu.Lock()
	locked := true
	defer func() {
		if locked {
			cold.mu.Unlock()
		}
	}()
	if owner.cold != cold || cold.operation != operation || operation != owner.operation || operation.nativeColdResume != cold || !cold.bound || !cold.handed || cold.closed || cold.startup != launch || launch.operation != operation || launch.unit == nil || cold.creation == nil || launch.nativePeer() == nil || launch.nativePeer().identity == nil || cold.artifacts == nil || cold.artifacts.slot == nil || cold.artifacts.slot.owner != owner {
		return rewindFailureV1("the reserved startup changed its actual Cold source, operation or native creator")
	}
	first, err := slotSnapshotPIDV1(cold.creation, true)
	if err != nil {
		return err
	}
	peer := launch.nativePeer()
	peer.mu.Lock()
	if peer.closed || peer.pidfd < 0 || peer.pid != first {
		peer.mu.Unlock()
		return rewindFailureV1("the reserved receiver differs from the original new native birth")
	}
	peer.mu.Unlock()
	if err := errors.Join(launch.check(ctx), launch.checkNativeMemoryV1(ctx, launch.manifest)); err != nil {
		return err
	}
	slot := cold.artifacts.slot
	slot.mu.Lock()
	defer slot.mu.Unlock()
	if slot.dispatched != nil {
		return rewindFailureV1("the original reserved startup loan was already attempted")
	}
	if err := slot.checkDiskV2(ctx, cold.artifacts, true); err != nil {
		return err
	}
	if err := cold.artifacts.checkRetainedBootDescriptorsV1(ctx); err != nil {
		return err
	}
	slot.dispatched = launch // A partial physical send may already loan the lease.
	// The physical writer re-enters the existing Cold native takeover gate.
	// Keep the actual source/slot fence, but do not recursively hold Cold.mu.
	cold.mu.Unlock()
	locked = false
	result := send(append([]*os.File(nil), slot.roles...))
	current, checkErr := slotSnapshotPIDV1(cold.creation, true)
	if checkErr != nil || current != first {
		return errors.Join(result, checkErr, rewindFailureV1("the original reserved receiver ended during its native loan"))
	}
	return errors.Join(result, ctx.Err())
}
