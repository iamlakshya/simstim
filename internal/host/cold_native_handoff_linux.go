//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"

	"golang.org/x/sys/unix"
)

// checkColdRestartReservationLockedV1 reuses only this original native holder
// vacancy. Its caller already owns the actual placement accounting gate.
func checkColdRestartReservationLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest) (bool, error) {
	if operation == nil || operation.nativeColdResume == nil {
		return false, nil
	}
	cold := operation.nativeColdResume
	if err := cold.CheckManifest(ctx, operation, next); err != nil {
		return true, err
	}
	return true, nil
}

// consumeColdResumeDispatchLockedV1 is called ONLY immediately before the
// actual first native dispatch, inside its original publication gate. The
// same owning OP remains locked while its vacancy holder gives way to the
// new tracked native creation. A token, Starting row or ACK cannot call it.
func consumeColdResumeDispatchLockedV1(ctx context.Context, operation *SessionOperation, publication *runtimePublication) error {
	if operation == nil || operation.nativeColdResume == nil {
		return nil
	}
	cold := operation.nativeColdResume
	cold.mu.Lock()
	defer cold.mu.Unlock()
	launch := operation.startup
	if !cold.bound || cold.handed || cold.closed || cold.operation != operation || launch == nil || launch.operation != operation || launch.placement != publication || publication == nil || publication.startup != operation || launch.started || launch.loanSent || launch.manager == nil {
		return auxiliaryUnavailableV1("the original cold source has no exact new native dispatch owner")
	}
	if err := cold.checkLockedV1(ctx, operation); err != nil {
		return err
	}
	fromStopped, err := consumeStoppedColdHoldersV1(ctx, operation, cold)
	if err != nil {
		return err
	}
	if !fromStopped {
		if err := cold.vacancy.holder.Close(); err != nil {
			return err
		}
		cold.vacancy.holder = nil
		cold.vacancy.transferred = true
	}
	cold.startup = launch
	cold.handed = true
	return ctx.Err()
}

func (cold *ColdResumeV1) checkNativeTakeoverLockedV1(ctx context.Context, operation *SessionOperation) error {
	if cold.custody != nil {
		return cold.custody.checkClaimedV1(ctx)
	}
	launch := cold.startup
	if !cold.bound || !cold.handed || cold.closed || operation != cold.operation || launch == nil || operation.startup != launch || launch.operation != operation || launch.placement == nil || launch.placement.startup != operation || launch.unit == nil || launch.nativePeer() == nil || launch.nativePeer().identity == nil || launch.line == nil {
		return auxiliaryUnavailableV1("the cold continuation has no actual original native takeover")
	}
	if BootID() != cold.vacancy.boot {
		return auxiliaryUnavailableV1("the native boot epoch changed during cold startup")
	}
	if err := launch.check(ctx); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current.RuntimeManifest, launch.manifest.RuntimeManifest) {
		return auxiliaryUnavailableV1("the original cold Need or runtime changed before native takeover")
	}
	if !bytes.Equal(current.VMNeedV1, cold.vacancy.need) {
		if err := checkColdRestartSelectionV1(operation, cold.vacancy.need, cold.vacancy.runtime, current); err != nil {
			return err
		}
	}
	if err := cold.artifacts.checkAfterNativeTakeoverV1(ctx, cold, launch); err != nil {
		return err
	}
	return errors.Join(launch.checkNativeMemoryV1(ctx, current), ctx.Err())
}

// checkColdNativeTakeoverLockedV1 is consumed by the actual OP sender only
// after its original code/namespace/current-sender and native cap checks.
func checkColdNativeTakeoverLockedV1(ctx context.Context, operation *SessionOperation) error {
	if operation == nil || operation.nativeColdResume == nil {
		return nil
	}
	cold := operation.nativeColdResume
	cold.mu.Lock()
	defer cold.mu.Unlock()
	return cold.checkNativeTakeoverLockedV1(ctx, operation)
}

// retainColdCreationV1 duplicates only the genuine first native activation's
// still-held original pidfd, before any parent cleanup can close it. No later
// PID lookup or stale integer can renew a failed creation source.
func retainColdCreationV1(operation *SessionOperation, launch *startupLaunchV1) error {
	if operation == nil || operation.nativeColdResume == nil {
		return nil
	}
	cold := operation.nativeColdResume
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if !cold.handed || cold.startup != launch || operation.startup != launch || launch.unit == nil || launch.unit.peer == nil || cold.creation != nil {
		return auxiliaryUnavailableV1("the original cold creation cannot be retained")
	}
	peer := launch.unit.peer
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if peer.closed || peer.pidfd < 0 {
		return auxiliaryUnavailableV1("the actual original cold creation ended before its retention")
	}
	fd, err := unix.FcntlInt(uintptr(peer.pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	cold.creation = os.NewFile(uintptr(fd), "original-cold-creation")
	return nil
}
