//go:build linux

package host

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

// OrdinaryPurgePreparationV2 retains the actual active ordinary holder under
// its original operation. It has no stopped, walled or storage authority.
type OrdinaryPurgePreparationV2 struct {
	mu                  sync.Mutex
	owner               *ordinaryHolderCaptureV2
	next                uint64
	quiesced, restored  bool
	transferred, closed bool
}

// PrepareOrdinaryPurgeForOperationV2 captures the original native producer
// without stopping it. Only a physical commit and join can issue a stopped
// subject. The current bounded producer pauses only its original main (N5).
func PrepareOrdinaryPurgeForOperationV2(ctx context.Context, operation *SessionOperation) (*OrdinaryPurgePreparationV2, error) {
	owner, err := captureOrdinaryHolderV2(ctx, operation, true)
	if owner == nil {
		return nil, err
	}
	// Adopt the actual partial capture BEFORE handling its error. An unready
	// object grants nothing, but its original native cleanup stays reachable.
	return &OrdinaryPurgePreparationV2{owner: owner, next: 2}, err
}

func (preparation *OrdinaryPurgePreparationV2) checkOriginalV2(ctx context.Context, operation *SessionOperation) error {
	if preparation == nil || ctx == nil || operation == nil || preparation.owner == nil || preparation.closed || preparation.transferred || preparation.restored {
		return sessionLeaseError("the original active ordinary preparation ended")
	}
	owner := preparation.owner
	if operation != owner.operation || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || owner.stopSent || owner.settled || owner.closed || owner.restored == nil || owner.holder == nil || owner.line == nil || owner.peer == nil || owner.origin == nil || owner.main == nil || owner.joined == nil {
		return sessionLeaseError("the active ordinary preparation lost its same original operation")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.holder.root), owner.checkOriginalSocketV1(), owner.checkCurrentOrdinaryV1(ctx), ordinaryJoinLatchV1(owner.joined, false), ordinaryJoinLatchV1(owner.restored, false)); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !ordinaryManifestV1(current) || current.Run.BootID != owner.boot || current.Cwd != owner.source.Cwd || !reflect.DeepEqual(current.Agent, owner.source.Agent) || !reflect.DeepEqual(current.Git, owner.source.Git) || !reflect.DeepEqual(current.Limits, owner.source.Limits) {
		return sessionLeaseError("the protected original active ordinary source changed")
	}
	return nil
}

func (preparation *OrdinaryPurgePreparationV2) requestV2(ctx context.Context, operation string) error {
	if preparation.next < 2 {
		return sessionLeaseError("the original ordinary continuation exhausted its request sequence")
	}
	id := preparation.next
	preparation.next++
	err := preparation.owner.ordinaryRequestV1(ctx, id, operation, preparation.owner.state)
	if err != nil && !preparation.owner.stopSent {
		// A cancelled or partial request cannot leave this holder waiting for
		// another request while its original main remains paused. Retain the
		// peer loan and cancel only this actual physical handler connection.
		_ = preparation.owner.line.conn.Close()
	}
	return err
}

// Check joins a fresh current-holder acknowledgement with the original
// operation, native roots, process/code owner and unconsumed completion loans.
func (preparation *OrdinaryPurgePreparationV2) Check(ctx context.Context, operation *SessionOperation) error {
	if preparation == nil {
		return sessionLeaseError("the actual active ordinary preparation is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if err := preparation.checkOriginalV2(ctx, operation); err != nil {
		return err
	}
	return preparation.requestV2(ctx, ordinaryPurgeCheckV2)
}

// Quiesce asks the genuine creator to retain and pause its original main.
// Its real handler restores that creation on cancellation or parent death.
// Managed whole-unit quiescence has a distinct outside restoration producer.
func (preparation *OrdinaryPurgePreparationV2) Quiesce(ctx context.Context, operation *SessionOperation) error {
	if preparation == nil {
		return sessionLeaseError("the actual active ordinary preparation is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if err := preparation.checkOriginalV2(ctx, operation); err != nil {
		return err
	}
	if preparation.quiesced {
		return preparation.requestV2(ctx, ordinaryPurgeCheckQuiescedV2)
	}
	if err := preparation.requestV2(ctx, ordinaryPurgeQuiesceV2); err != nil {
		return err
	}
	preparation.quiesced = true
	return preparation.checkOriginalV2(ctx, operation)
}

// CheckQuiesced observes the same original creator's current native pause.
// The local phase field cannot substitute for that physical response.
func (preparation *OrdinaryPurgePreparationV2) CheckQuiesced(ctx context.Context, operation *SessionOperation) error {
	if preparation == nil {
		return sessionLeaseError("the actual active ordinary preparation is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if err := preparation.checkOriginalV2(ctx, operation); err != nil {
		return err
	}
	return preparation.requestV2(ctx, ordinaryPurgeCheckQuiescedV2)
}

// Restore ends only this genuine reversible preparation. A lost response
// closes its actual transport and joins the original holder's restore latch.
// It never continues a pre-existing user-stopped main or recreates an agent.
func (preparation *OrdinaryPurgePreparationV2) Restore(ctx context.Context, operation *SessionOperation) error {
	if preparation == nil {
		return sessionLeaseError("the original ordinary restoration is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	return preparation.restoreV2(ctx, operation)
}

func (preparation *OrdinaryPurgePreparationV2) restoreV2(ctx context.Context, operation *SessionOperation) error {
	if preparation.owner == nil || operation == nil || operation != preparation.owner.operation || preparation.closed || preparation.transferred || preparation.owner.stopSent {
		return sessionLeaseError("the ordinary preparation cannot restore after its actual stop dispatch")
	}
	if preparation.restored {
		return preparation.checkRestoredV2(ctx, operation)
	}
	if ctx == nil {
		return sessionLeaseError("the original ordinary restoration has no request lifetime")
	}
	owner := preparation.owner
	requestErr := preparation.requestV2(ctx, ordinaryPurgeRestoreV2)
	// The original native peer loan remains open while closing only the
	// physical transport cancels its already-owned restoration handler.
	if requestErr != nil {
		_ = owner.line.conn.Close()
	}
	for ordinaryJoinLatchV1(owner.restored, true) != nil {
		// An unknown original restoration retains the actual OP and loans.
		// Neither a timeout nor parent death is a successful acknowledgement.
		time.Sleep(10 * time.Millisecond)
	}
	preparation.restored = true
	return errors.Join(requestErr, preparation.checkRestoredV2(context.Background(), operation))
}

// CommitStop dispatches only after the original creator positively checks its
// held pause. The exact capture becomes stopped ownership only after its real
// main, terminal, reader, VT and holder have joined. No status cast is used.
func (preparation *OrdinaryPurgePreparationV2) CommitStop(ctx context.Context, operation *SessionOperation) (*StoppedOrdinarySessionV1, error) {
	if preparation == nil {
		return nil, sessionLeaseError("the original ordinary preparation is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if err := preparation.checkOriginalV2(ctx, operation); err != nil {
		return nil, err
	}
	if err := preparation.requestV2(ctx, ordinaryPurgeCheckQuiescedV2); err != nil {
		return nil, err
	}
	err := preparation.requestV2(ctx, ordinaryStopCommitV1)
	if !preparation.owner.stopSent {
		return nil, err
	}
	if joinErr := preparation.owner.settleOrdinaryV1(); joinErr != nil {
		return nil, errors.Join(err, joinErr)
	}
	stopped := &StoppedOrdinarySessionV1{ordinaryHolderCaptureV2: preparation.owner}
	if checkErr := stopped.Check(context.Background(), operation); checkErr != nil {
		return nil, errors.Join(err, checkErr)
	}
	preparation.transferred = true
	return stopped, err
}

// Close restores and joins its own preparation before releasing original
// loans. A transferred stopped owner retains those loans through cleanup.
func (preparation *OrdinaryPurgePreparationV2) Close() error {
	if preparation == nil {
		return nil
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if preparation.closed || preparation.transferred {
		return nil
	}
	if preparation.owner == nil {
		return sessionLeaseError("the ordinary preparation has no original native capture")
	}
	if preparation.owner.stopSent {
		// The transaction must take the genuine stopped owner and perform its
		// outstanding cleanup. Close is never post-stop release permission.
		return sessionLeaseError("the ordinary preparation retains its dispatched native stop and cleanup")
	}
	if !preparation.owner.pauseSent {
		// The same producer records the physical quiesce send boundary. A
		// partial/unused capture cannot have paused its original subject.
		err := preparation.owner.Close()
		if err == nil {
			preparation.closed = true
		}
		return err
	}
	err := preparation.restoreV2(context.Background(), preparation.owner.operation)
	if !preparation.restored {
		return err
	}
	preparation.closed = true
	return errors.Join(err, preparation.owner.Close())
}

func (preparation *OrdinaryPurgePreparationV2) checkRestoredV2(ctx context.Context, operation *SessionOperation) error {
	if ctx == nil || operation == nil || preparation.owner == nil || preparation.owner.operation != operation || operation.native == nil || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("the original ordinary restoration lost its same operation")
	}
	owner := preparation.owner
	if owner.closed || owner.stopSent || owner.settled || owner.restored == nil || owner.holder == nil || owner.peer == nil || owner.origin == nil {
		return sessionLeaseError("the original restoration native objects ended")
	}
	return errors.Join(ctx.Err(), CheckCaller(), ordinaryJoinLatchV1(owner.restored, true), ordinaryJoinLatchV1(owner.joined, false), owner.holder.check(ctx), owner.checkCurrentOrdinaryV1(ctx), operation.native.rootRef.CheckRoot(owner.holder.root))
}
