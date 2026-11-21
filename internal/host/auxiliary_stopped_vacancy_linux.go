//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// captureAuxiliaryVacancyFromStoppedV1 consumes the actual same-boot whole-stop
// owner. Its private holder copy is the SAME still-locked OFD, with a separate
// checked root. Closing either owner cannot revoke the other's description.
// This supplies no execution authority: reservation and NEW native activation
// still pass the existing artifact, accounting and private startup-loan checks.
func captureAuxiliaryVacancyFromStoppedV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) (_ *auxiliaryVacancyV1, returned error) {
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	stopped.mu.Lock()
	defer stopped.mu.Unlock()
	if stopped.closed || !stopped.settled || stopped.operation != operation || stopped.holder == nil || stopped.origin == nil || BootID() != stopped.boot {
		return nil, auxiliaryUnavailableV1("the original stopped owner ended before auxiliary capture")
	}
	if err := errors.Join(stopped.origin.checkIdentity(), stopped.checkStoppedNativeFactsV1(ctx)); err != nil {
		return nil, err
	}
	snapshot, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.ID != operation.Primary() || snapshot.SandboxOf != "" || snapshot.Wall == nil || snapshot.Run.BootID != stopped.boot || snapshot.Auxiliary != nil {
		return nil, auxiliaryUnavailableV1("the original whole-stop source changed before auxiliary capture")
	}
	if err := ValidateManifestStart(snapshot); err != nil {
		return nil, err
	}
	file, err := duplicateAuxiliaryHolderV1(ctx, stopped.holder)
	if err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(stopped.holder.path, false)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	holder := &sessionFileLease{root: root, file: file, rootRef: stopped.holder.rootRef, fileRef: stopped.holder.fileRef, id: stopped.holder.id, path: stopped.holder.path, name: holderLockFile}
	result := &auxiliaryVacancyV1{stopped: stopped, stoppedOriginalHolder: stopped.holder, stoppedVacancyHolder: holder, operation: operation, holder: holder, primary: UnitName(operation.Primary()), boot: stopped.boot, need: bytes.Clone(snapshot.VMNeedV1)}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, result.Close())
		}
	}()
	if err := errors.Join(holder.check(ctx), operation.native.rootRef.CheckRoot(holder.root)); err != nil {
		return nil, err
	}
	result.auxiliary, err = AuxUnitNameV1(operation.Primary())
	if err != nil {
		return nil, err
	}
	result.runtime, err = startupRuntimeBytes(snapshot)
	if err != nil {
		return nil, err
	}
	// Retain a separate actual manager connection. The existing stop owner may
	// keep its original connection until its own final physical check and close.
	result.manager, err = openNativeUserManagerV1(ctx)
	if err != nil {
		return nil, err
	}
	if err := result.Check(ctx); err != nil {
		return nil, err
	}
	if err := stopped.checkStoppedNativeFactsV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}
