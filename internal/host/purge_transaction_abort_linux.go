//go:build linux

package host

import (
	"context"
	"os"
)

// The actual restoration checks run before constructing this one closure
// vector. Once native Close starts, retries observe those same descriptors;
// they never ask a released preparation to manufacture another restoration.
func newPurgeAbortClosureV3(ctx context.Context, transaction *purgeActiveTransactionV2) (*purgeTransactionClosureV3, error) {
	if ctx == nil || transaction == nil || transaction.closed || transaction.anyIrreversibleV2() {
		return nil, placementError("the original abort has no reversible actual transaction")
	}
	for _, subject := range transaction.subjects {
		if subject.preparation == nil || subject.preparation.owner == nil {
			continue
		}
		preparation := subject.preparation
		if preparation.owner.pauseSent {
			if err := preparation.checkRestoredV2(ctx, subject.operation); err != nil {
				return nil, err
			}
		}
	}
	owner := &purgeTransactionClosureV3{transaction: transaction}
	for _, subject := range transaction.subjects {
		owner.appendReadStepsV3(subject)
		if subject.worktree != nil {
			owner.addStepV3(subject.worktree.Close, nil, []*os.Root{subject.worktree.root, subject.worktree.parent})
		}
		if subject.preparation != nil && subject.preparation.owner != nil {
			capture := subject.preparation.owner
			files := []*os.File{capture.main, capture.joined, capture.restored}
			roots := []*os.Root{capture.socketRoot}
			if capture.holder != nil {
				files = append(files, capture.holder.file)
				roots = append(roots, capture.holder.root)
			}
			if capture.peer != nil {
				roots = append(roots, capture.peer.proc)
			}
			if capture.line != nil && capture.line.nativePeer != nil {
				roots = append(roots, capture.line.nativePeer.proc)
			}
			owner.addStepV3(subject.preparation.Close, files, roots)
		}
	}
	if transaction.owner != nil {
		owner.addStepV3(transaction.withdrawRestoredDrainV3, nil, nil)
	}
	for _, subject := range transaction.subjects {
		if subject.operation != nil && subject.operation.native != nil {
			owner.addStepV3(subject.operation.CloseWithError, []*os.File{subject.operation.native.file}, []*os.Root{subject.operation.native.root})
		}
	}
	if transaction.origin != nil {
		var files []*os.File
		var roots []*os.Root
		files = append(files, transaction.origin.code)
		for _, file := range transaction.origin.files {
			files = append(files, file.file)
		}
		for _, root := range transaction.origin.roots {
			roots = append(roots, root.root)
		}
		owner.addStepV3(transaction.origin.Close, files, roots)
	}
	if transaction.owner != nil {
		var roots []*os.Root
		for _, pin := range transaction.owner.roots {
			if pin != nil {
				roots = append(roots, pin.root)
			}
		}
		owner.addStepV3(transaction.closeOwnerRootsV2, nil, roots)
		owner.addStepV3(transaction.owner.release, nil, nil)
	}
	return owner, nil
}

func (transaction *purgeActiveTransactionV2) withdrawRestoredDrainV3() error {
	if transaction == nil || transaction.owner == nil || transaction.abortClosure == nil || transaction.abortClosure.transaction != transaction || transaction.anyIrreversibleV2() {
		return placementError("the drain withdrawal has no SAME actually restored closure vector")
	}
	owner := transaction.owner
	return owner.placement.withControlRoot(context.Background(), func(control *placementControl, root *os.Root) error {
		if control.Owner == nil || *control.Owner != owner.process || control.OwnerRun != owner.run || control.Destructive {
			if len(transaction.subjects) == 0 && transaction.origin == nil && len(owner.roots) == 0 {
				// The original begin refused before this object could capture ANY
				// session/root subject. Preserve the other owner's control intact;
				// releasing this unused issuer grants no authority over it.
				return nil
			}
			return placementError("the original restored owner lost its exact native control binding")
		}
		if control.Drain == "" && control.Nonce == "" && control.Phase == "" && control.Report == nil {
			// An earlier physical write by this STILL original owner may have
			// completed before its directory sync reported an error. No subject
			// is adopted from this read; their genuine closure vector is held.
			return syncCapturedDirectory(root)
		}
		if control.Drain != "purge" || control.Nonce != owner.args.Nonce {
			return placementError("another drain replaced the original reversible removal")
		}
		control.Drain, control.Nonce, control.Phase = "", "", ""
		control.Report = nil
		return writePlacementControl(root, *control)
	}, false)
}
