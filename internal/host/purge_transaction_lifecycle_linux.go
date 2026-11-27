//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

type purgeActualTransactionKeyV2 struct{}

func (transaction *purgeActiveTransactionV2) beforeStopFailureV2(cause error) (report api.PurgeResult, returned error) {
	// The SAME actual transaction remains reachable on every error.
	transaction.pendingAbort = errors.Join(transaction.pendingAbort, cause)
	if transaction.anyIrreversibleV2() {
		return transaction.reportV2(), cause
	}
	err := transaction.abortBeforeStopLockedV2(context.Background())
	return transaction.reportV2(), errors.Join(cause, err)
}

// AbortBeforeStop denies release from actual producer dispatch or an invoked
// native global-cleanup owner. It cannot infer eligibility from a saved phase.
func (transaction *purgeActiveTransactionV2) AbortBeforeStop(ctx context.Context) error {
	if transaction == nil || ctx == nil {
		return placementError("the actual purge abort has no original transaction")
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	return transaction.abortBeforeStopLockedV2(ctx)
}

func (transaction *purgeActiveTransactionV2) abortBeforeStopLockedV2(ctx context.Context) error {
	if transaction.closed {
		return placementError("the original purge transaction is closed")
	}
	if transaction.anyIrreversibleV2() {
		return placementError("the actual purge has dispatched irreversible native work and retains its original cleanup")
	}
	if transaction.aborted {
		return nil
	}
	if transaction.abortClosure == nil {
		var failure error
		for index := len(transaction.subjects) - 1; index >= 0; index-- {
			subject := transaction.subjects[index]
			if subject.preparation == nil || subject.preparation.owner == nil || !subject.preparation.owner.pauseSent {
				continue
			}
			failure = errors.Join(failure, subject.preparation.Restore(ctx, subject.operation))
		}
		if failure != nil {
			return failure
		}
		closure, err := newPurgeAbortClosureV3(ctx, transaction)
		if err != nil {
			return err
		}
		transaction.abortClosure = closure
	}
	if err := transaction.abortClosure.Continue(ctx); err != nil {
		return err
	}

	transaction.aborted = true
	return nil
}

func (transaction *purgeActiveTransactionV2) cleanupOrdinaryV2(ctx context.Context, subject *purgeOrdinarySubjectV2) error {
	if subject == nil || subject.operation == nil || subject.manifest == nil || subject.stopped == nil {
		return sessionLeaseError("the actual stopped cleanup vector is incomplete")
	}
	if subject.terminal != nil && !subject.terminal.ready {
		if err := subject.terminal.Close(); err != nil {
			return err
		}
		subject.terminal = nil
	}
	if subject.terminal == nil {
		if err := subject.stopped.Check(ctx, subject.operation); err != nil {
			return err
		}
		if err := transaction.markOrdinaryStoppedV2(ctx, subject); err != nil {
			return err
		}
		if err := transaction.owner.replayResource(ctx); err != nil {
			return err
		}
		if err := transaction.owner.destroyOrdinarySessionResourcesV1(subject.operation, subject.stopped); err != nil {
			return err
		}
		if subject.worktree != nil {
			if err := subject.worktree.Continue(ctx, subject.stopped, subject.workLoss); err != nil {
				return err
			}
		}
		terminal, err := newPurgeOrdinaryTerminalV2(ctx, transaction.owner, subject.operation, subject.stopped)
		subject.terminal = terminal
		if err != nil {
			return err
		}
	}
	if err := subject.terminal.Continue(ctx); err != nil {
		return err
	}
	if err := subject.terminal.CheckComplete(ctx); err != nil {
		return err
	}
	return transaction.owner.gate(func(control *placementControl, _ *os.Root) error {
		plan, found := control.Sessions[subject.operation.Primary()]
		if !found || !samePurgeDirectoryRef(plan.Directory, subject.directory) {
			return sessionLeaseError("the completed original session changed its native bookkeeping binding")
		}
		if !plan.Cleaned {
			transaction.owner.reportItem(true, "session", subject.operation.Primary(), "")
		}
		plan.Cleaned = true
		control.Sessions[subject.operation.Primary()] = plan
		return nil
	})
}

func (transaction *purgeActiveTransactionV2) markOrdinaryStoppedV2(ctx context.Context, subject *purgeOrdinarySubjectV2) error {
	if err := subject.stopped.Check(ctx, subject.operation); err != nil {
		return err
	}
	ref, err := nativeWorktreeDirectoryRefV1(subject.operation.native.root)
	if err != nil {
		return err
	}
	if subject.directory.Inode != 0 && !samePurgeDirectoryRef(subject.directory, ref) {
		return sessionLeaseError("the actual stopped vector changed its original root")
	}
	subject.directory = ref
	return transaction.owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Sessions == nil {
			control.Sessions = map[string]purgeSessionPlan{}
		}
		prior, found := control.Sessions[subject.operation.Primary()]
		if found && !samePurgeDirectoryRef(prior.Directory, ref) {
			return sessionLeaseError("the original native stopped session plan was replaced")
		}
		prior.ID, prior.Name, prior.Directory, prior.Stopped = subject.operation.Primary(), subject.manifest.Name, ref, true
		control.Sessions[prior.ID], control.Destructive = prior, true
		return nil
	})
}

func (transaction *purgeActiveTransactionV2) anyIrreversibleV2() bool {
	return transaction.anyStopV2() || transaction.globalStarted
}

func (transaction *purgeActiveTransactionV2) closeOwnerRootsV2() error {
	var failure error
	for _, pin := range transaction.owner.roots {
		if pin == nil || pin.root == nil {
			continue
		}
		if err := pin.root.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			failure = errors.Join(failure, err)
		}
	}
	return failure
}

// CloseAfterCleanup requires actual terminal unlinks and the original native
// final-marker creator. A report's HostComplete/phase cannot release custody.
func (transaction *purgeActiveTransactionV2) CloseAfterCleanup(ctx context.Context) error {
	if transaction == nil || ctx == nil {
		return placementError("the actual purge final close is absent")
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return nil
	}
	if transaction.aborted {
		transaction.closed = true
		return nil
	}
	if transaction.closure == nil {
		closure, err := newPurgeTransactionClosureV3(ctx, transaction)
		if err != nil {
			return err
		}
		transaction.closure = closure
	}
	if err := transaction.closure.Continue(ctx); err != nil {
		return err
	}
	transaction.closed = true
	return nil
}
