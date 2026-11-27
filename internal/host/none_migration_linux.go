//go:build linux

package host

import (
	"context"
	"errors"
	"time"
)

func nowUTCForNoneV1() time.Time                { return time.Now().UTC() }
func validNoneMigrationBootV1(boot string) bool { return auxiliaryBootIDV1(boot) }

func captureLegacyNoneRebootV1(ctx context.Context, operation *SessionOperation) error {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || operation.noneMigration != nil {
		return sessionLeaseError("legacy reboot migration has no original owning operation")
	}
	if err := errors.Join(CheckCaller(), ctx.Err()); err != nil {
		return err
	}
	before, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	boot := BootID()
	if !ordinaryManifestV1(before) || !auxiliaryBootIDV1(boot) || !auxiliaryBootIDV1(before.Run.BootID) || before.Run.BootID == boot {
		return sessionLeaseError("the legacy holder's original lifetime needs a genuine stop or a changed kernel boot epoch")
	}
	holder, err := openOwnedSessionFileLease(ctx, operation.Primary(), holderLockFile, 0)
	if err != nil {
		return err
	}
	if err := errors.Join(operation.native.rootRef.CheckRoot(holder.root), holder.check(ctx)); err != nil {
		return errors.Join(err, holder.Close())
	}
	if BootID() != boot {
		return errors.Join(sessionLeaseError("the actual boot epoch changed during legacy capture"), holder.Close())
	}
	operation.noneMigration = &legacyNoneMigrationV1{operation: operation, before: CloneManifest(*before), boot: boot, holder: holder}
	return nil
}

func stopLegacyForNoneResumeV1(ctx context.Context, operation *SessionOperation) error {
	before, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if before.Wall != nil {
		return sessionLeaseError("a supervised source cannot enter the legacy migration path")
	}
	if before.Run.BootID != BootID() {
		return captureLegacyNoneRebootV1(ctx, operation)
	}
	stopped, err := StopOrdinarySessionForOperationV1(ctx, operation)
	if err != nil {
		if stopped != nil {
			return errors.Join(err, stopped.Close())
		}
		return err
	}
	if stopped == nil {
		return sessionLeaseError("the legacy source has no actual joined holder")
	}
	fresh, err := operation.LoadContext(ctx)
	if err != nil {
		return errors.Join(err, stopped.Close())
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return errors.Join(err, stopped.Close())
	}
	operation.noneMigration = &legacyNoneMigrationV1{operation: operation, before: CloneManifest(*fresh), boot: BootID(), stopped: stopped}
	return nil
}
