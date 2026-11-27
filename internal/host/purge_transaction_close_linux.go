//go:build linux

package host

import (
	"context"
	"errors"
	"os"
)

// This closure producer is constructed only after ALL same-owner physical
// terminal unlinks and the genuine final marker have passed. It retains the
// original objects through partial Close errors; a later call never tries to
// revalidate native subjects whose original descriptors it already closed.
type purgeTransactionClosureV3 struct {
	transaction *purgeActiveTransactionV2
	steps       []*purgeCloseStepV3
}

type purgeCloseStepV3 struct {
	close     func() error
	files     []*os.File
	roots     []*os.Root
	completed bool
}

func newPurgeTransactionClosureV3(ctx context.Context, transaction *purgeActiveTransactionV2) (*purgeTransactionClosureV3, error) {
	if transaction == nil || transaction.closed || transaction.owner == nil || transaction.finalGuard == nil || transaction.origin == nil {
		return nil, placementError("the actual purge has no complete original closure vector")
	}
	if err := transaction.finalGuard.Check(ctx); err != nil {
		return nil, err
	}
	for _, subject := range transaction.subjects {
		if subject == nil || subject.terminal == nil || subject.stopped == nil || subject.operation == nil || subject.operation.native == nil {
			return nil, sessionLeaseError("the actual final closure lost an original subject")
		}
		if err := subject.terminal.CheckComplete(ctx); err != nil {
			return nil, err
		}
		if subject.worktree != nil {
			if err := subject.worktree.CheckComplete(ctx); err != nil {
				return nil, err
			}
		}
	}
	owner := &purgeTransactionClosureV3{transaction: transaction}
	appendStep := func(close func() error, files []*os.File, roots []*os.Root) {
		owner.steps = append(owner.steps, &purgeCloseStepV3{close: close, files: files, roots: roots})
	}
	for _, subject := range transaction.subjects {
		var files []*os.File
		for _, entry := range subject.terminal.files {
			if entry.owned {
				files = append(files, entry.file)
			}
		}
		appendStep(subject.terminal.Close, files, []*os.Root{subject.terminal.parent})
		owner.appendReadStepsV3(subject)
		if subject.worktree != nil {
			appendStep(subject.worktree.Close, nil, []*os.Root{subject.worktree.root, subject.worktree.parent})
		}
		stopped := subject.stopped
		appendStep(stopped.Close, []*os.File{stopped.holder.file, stopped.main, stopped.joined, stopped.restored}, []*os.Root{stopped.holder.root, stopped.socketRoot, stopped.peer.proc})
		appendStep(subject.operation.CloseWithError, []*os.File{subject.operation.native.file}, []*os.Root{subject.operation.native.root})
	}
	appendStep(transaction.finalGuard.Close, []*os.File{transaction.finalGuard.marker}, nil)
	var files []*os.File
	var roots []*os.Root
	files = append(files, transaction.origin.code)
	for _, file := range transaction.origin.files {
		files = append(files, file.file)
	}
	for _, root := range transaction.origin.roots {
		roots = append(roots, root.root)
	}
	appendStep(transaction.origin.Close, files, roots)
	roots = nil
	for _, pin := range transaction.owner.roots {
		if pin != nil {
			roots = append(roots, pin.root)
		}
	}
	appendStep(transaction.closeOwnerRootsV2, nil, roots)
	// This final control operation has no incoming report or phase authority.
	// Its own unchanged original-owner gate performs the actual release.
	appendStep(transaction.owner.release, nil, nil)
	return owner, nil
}

func (owner *purgeTransactionClosureV3) Continue(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.transaction == nil || owner.transaction.closure != owner && owner.transaction.abortClosure != owner {
		return placementError("the final closure is not this SAME original transaction's native vector")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	for _, step := range owner.steps {
		if step.completed {
			if err := step.checkClosedV3(); err != nil {
				return err
			}
			continue
		}
		err := step.close()
		observed := step.checkClosedV3()
		if observed == nil && (err == nil || len(step.files)+len(step.roots) != 0) {
			step.completed = true
		}
		if err != nil || observed != nil {
			return errors.Join(err, observed)
		}
	}
	return nil
}

func (step *purgeCloseStepV3) checkClosedV3() error {
	for _, file := range step.files {
		if file == nil {
			continue
		}
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			return errors.Join(placementError("an original final file descriptor has not actually closed"), err)
		}
	}
	for _, root := range step.roots {
		if root == nil {
			continue
		}
		file, err := root.Open(".")
		if file != nil {
			_ = file.Close()
		}
		if !errors.Is(err, os.ErrClosed) {
			return errors.Join(placementError("an original final directory descriptor has not actually closed"), err)
		}
	}
	return nil
}

func (owner *purgeTransactionClosureV3) addStepV3(close func() error, files []*os.File, roots []*os.Root) {
	owner.steps = append(owner.steps, &purgeCloseStepV3{close: close, files: files, roots: roots})
}

func (owner *purgeTransactionClosureV3) appendReadStepsV3(subject *purgeOrdinarySubjectV2) {
	for _, inspection := range []*joinedOrdinaryGitInspectionV2{subject.initial, subject.final} {
		if inspection == nil {
			continue
		}
		files := []*os.File{inspection.creation, inspection.code}
		var roots []*os.Root
		if inspection.endpoint != nil {
			files = append(files, inspection.endpoint.socketFile, inspection.endpoint.directory)
			roots = append(roots, inspection.endpoint.root)
		}
		if inspection.peer != nil {
			roots = append(roots, inspection.peer.proc)
		}
		if inspection.line != nil && inspection.line.nativePeer != nil {
			roots = append(roots, inspection.line.nativePeer.proc)
		}
		if inspection.roots != nil {
			files = append(files, inspection.roots.Work, inspection.roots.Admin, inspection.roots.Common)
		}
		owner.addStepV3(inspection.Close, files, roots)
	}
	if subject.permit != nil {
		permit := subject.permit
		roots := []*os.Root{permit.work, permit.parent, permit.repository, permit.common}
		var files []*os.File
		if permit.mapping != nil {
			roots = append(roots, permit.mapping.admin)
			for _, control := range permit.mapping.files {
				files = append(files, control.file)
			}
		}
		owner.addStepV3(permit.Close, files, roots)
	}
}
