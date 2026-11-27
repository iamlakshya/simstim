//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// noneAutomaticRestoreOriginV1 is issued only by the real restore controller.
// It retains its actual global native gate and SAME session operation. A
// selection DTO, boot string or context marker cannot issue this origin.
type noneAutomaticRestoreOriginV1 struct {
	self               *noneAutomaticRestoreOriginV1
	gate               *restoreGateV1
	operation          *SessionOperation
	source             *NoneColdResumeV1
	boot, previousBoot string
	failure            error
}

func captureNoneAutomaticContinuationV1(ctx context.Context, operation *SessionOperation, gate *restoreGateV1) (*NoneColdResumeV1, error) {
	origin := &noneAutomaticRestoreOriginV1{gate: gate, operation: operation, boot: BootID()}
	origin.self = origin
	if err := origin.checkGateV1(ctx); err != nil {
		return nil, err
	}
	before, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !noneColdRoleV1(before) || !auxiliaryBootIDV1(before.Run.BootID) || before.Run.BootID == origin.boot {
		return nil, sessionLeaseError("automatic None restore has no genuine prior kernel boot")
	}
	origin.previousBoot = before.Run.BootID
	cold, err := captureNoneColdOwnerV1(ctx, operation, nil, origin)
	origin.source = cold
	if err != nil {
		origin.failure = err
		return cold, errors.Join(err, cold.CloseDiagnosticsV4())
	}
	if err := cold.Check(ctx, operation); err != nil {
		origin.failure = err
		cleanup := cold.Close()
		if cleanup != nil {
			return cold, errors.Join(err, cleanup, cold.CloseDiagnosticsV4())
		}
		return nil, errors.Join(err, cold.CloseDiagnosticsV4())
	}
	return cold, nil
}

func (origin *noneAutomaticRestoreOriginV1) checkGateV1(ctx context.Context) error {
	if origin == nil || origin.self != origin || ctx == nil || origin.gate == nil || origin.gate.publisher == nil || origin.gate.publisher.ctx == nil || origin.operation == nil || origin.operation.native == nil || origin.operation.native.borrowed || !origin.operation.Valid(origin.operation.Primary()) || origin.operation.ownerContext != origin.gate.publisher.ctx || !auxiliaryBootIDV1(origin.boot) || origin.boot != BootID() {
		return sessionLeaseError("the automatic None origin lost its actual restore gate or original owning OP")
	}
	if err := errors.Join(CheckCaller(), origin.gate.Check(), origin.operation.native.check(ctx), ctx.Err()); err != nil {
		return err
	}
	// The real coordinator was locked by this current outside process. This
	// is its original global gate OFD, never a recreated path or copied plan.
	return checkBorrowedFlockV1(origin.gate.file, startupCredentials{pid: os.Getpid(), uid: uint32(os.Getuid())})
}

func (origin *noneAutomaticRestoreOriginV1) checkV1(ctx context.Context, cold *NoneColdResumeV1) error {
	if origin != nil && origin.failure != nil {
		return errors.Join(sessionLeaseError("the automatic None capture is cleanup-only after its original failure"), origin.failure)
	}
	if cold == nil || origin == nil || origin.source != cold || cold.self != cold || cold.automatic != origin || cold.preparation != nil || cold.operation != origin.operation || cold.sameBoot || cold.boot != origin.boot || cold.source.Run.BootID != origin.previousBoot || origin.previousBoot == origin.boot {
		return sessionLeaseError("the automatic None continuation changed its concrete original producer")
	}
	return origin.checkGateV1(ctx)
}

// restoreFromGateV1 is a distinct real automatic consumer. It binds no bridge
// token, VM artefact, declared quantum or copied automatic selection.
func (cold *NoneColdResumeV1) restoreFromGateV1(ctx context.Context, operation *SessionOperation) ([]string, string, error) {
	if cold == nil || cold.automatic == nil {
		return nil, "", sessionLeaseError("automatic None restore has no actual gate-bound continuation")
	}
	if err := cold.Check(ctx, operation); err != nil {
		return nil, "", err
	}
	root, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, "", err
	}
	if !noneColdRoleV1(root) || root.Run.BootID == BootID() {
		return nil, "", sessionLeaseError("automatic None restore lost its genuine prior boot and original role")
	}
	if err := admitRestoreMemoryV1(ctx, operation); err != nil {
		return nil, "", err
	}
	if err := applyAdmittedRestartMemoryV1(operation, root); err != nil {
		return nil, "", err
	}
	if err := cold.CheckManifest(ctx, operation, root); err != nil {
		return nil, "", err
	}
	cold.mu.Lock()
	if cold.bound || cold.closed {
		cold.mu.Unlock()
		return nil, "", sessionLeaseError("the automatic None continuation was already consumed")
	}
	operation.gate.Lock()
	if !operation.active.Load() || operation.noneCold != nil || operation.startup != nil {
		operation.gate.Unlock()
		cold.mu.Unlock()
		return nil, "", sessionLeaseError("the restore operation already owns another actual creator")
	}
	operation.noneCold = cold
	cold.bound = true
	operation.gate.Unlock()
	cold.mu.Unlock()
	if err := cold.cleanupPreviousNoneDirectoryV1(ctx); err != nil {
		return nil, "", err
	}
	root.Run.RespawnCount++
	return startAgain(root, again{operation: operation, label: restoredLabel, reason: "unwalled sessions restored after a host restart", allowMissing: true})
}
