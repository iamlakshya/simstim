//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"reflect"
)

// StoppedOrdinarySessionV1 owns only the actual ordinary holder/main/terminal
// closure. It has no conversion to StoppedSessionV1 or VM/slot authority.
type StoppedOrdinarySessionV1 struct{ *ordinaryHolderCaptureV2 }

// StopOrdinarySessionForOperationV1 captures the genuine current producer
// under the SAME owning OP/root, then joins its actual objects. Unsupported
// legacy or unknown current lifetimes remain preserved even with Force.
func StopOrdinarySessionForOperationV1(ctx context.Context, operation *SessionOperation) (_ *StoppedOrdinarySessionV1, returned error) {
	owner, err := captureOrdinaryHolderV1(ctx, operation, false)
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	if err := errors.Join(ctx.Err(), owner.checkOriginalSocketV1(), owner.checkCurrentOrdinaryV1(ctx)); err != nil {
		return nil, err
	}
	err = owner.ordinaryRequestV1(ctx, 2, ordinaryStopCommitV1, owner.state)
	if !owner.stopSent {
		return nil, err
	}
	if settleErr := owner.settleOrdinaryV1(); settleErr != nil {
		return nil, errors.Join(err, settleErr)
	}
	stopped := &StoppedOrdinarySessionV1{ordinaryHolderCaptureV2: owner}
	if checkErr := stopped.Check(context.Background(), operation); checkErr != nil {
		return nil, errors.Join(err, checkErr)
	}
	complete = true
	return stopped, err
}

// Check requires the exact original operation and root, including all current
// named bindings. No copied status or public record can create this subject.
func (owner *StoppedOrdinarySessionV1) Check(ctx context.Context, operation *SessionOperation) error {
	if owner == nil || owner.ordinaryHolderCaptureV2 == nil {
		return sessionLeaseError("the original ordinary stopped owner is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if ctx == nil || operation == nil || operation != owner.operation || !owner.settled || owner.closed || owner.holder == nil || owner.origin == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("the original ordinary stopped operation ended or changed")
	}
	if err := errors.Join(CheckCaller(), owner.origin.checkIdentity(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root)); err != nil {
		return err
	}
	fresh, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !ordinaryManifestV1(fresh) || fresh.Cwd != owner.source.Cwd || !reflect.DeepEqual(fresh.Agent, owner.source.Agent) || !reflect.DeepEqual(fresh.Git, owner.source.Git) || !reflect.DeepEqual(fresh.Limits, owner.source.Limits) {
		return sessionLeaseError("the protected original ordinary stop source changed")
	}
	return owner.checkStoppedOrdinaryNativeFactsV1(ctx)
}

// This final native-only check survives deliberate unlinking of the original
// named metadata/OP/holder files by the separate explicit storage consumer.
func (owner *StoppedOrdinarySessionV1) checkStoppedOrdinaryNativeFactsV1(ctx context.Context) error {
	if owner == nil || owner.ordinaryHolderCaptureV2 == nil || ctx == nil || !owner.settled || owner.closed || owner.operation == nil || owner.operation.native == nil || !owner.operation.active.Load() || owner.holder == nil || owner.holder.file == nil || owner.origin == nil || owner.boot != BootID() {
		return sessionLeaseError("the original bounded ordinary stop source ended")
	}
	actualHolder, err := owner.holder.file.Stat()
	if err != nil || owner.holderInfo == nil || !os.SameFile(actualHolder, owner.holderInfo) {
		return errors.Join(sessionLeaseError("the retained original ordinary holder description changed"), err)
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), checkOrdinaryPurgeOriginV2(ctx, owner.ordinaryHolderCaptureV2), owner.operation.native.rootRef.CheckRoot(owner.holder.root), ordinaryJoinLatchV1(owner.joined, true), ordinaryPidfdExitedV1(owner.main), ordinaryPeerExitedV1(owner.peer)); err != nil {
		return err
	}
	if owner.managed != nil {
		return owner.managed.stopped(ctx)
	}
	return nil
}
func (owner *StoppedOrdinarySessionV1) ordinaryScopeV1() string {
	if owner != nil && owner.ordinaryHolderCaptureV2 != nil && owner.managed != nil {
		return "managed whole unit"
	}
	return "original holder, main process and terminal (N5)"
}
func (owner *StoppedOrdinarySessionV1) Close() error {
	if owner == nil || owner.ordinaryHolderCaptureV2 == nil {
		return nil
	}
	return owner.ordinaryHolderCaptureV2.Close()
}
