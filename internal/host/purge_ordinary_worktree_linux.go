//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// This storage owner requires the distinct ordinary native subject and the
// actual pre-checkout directory creator. It grants no external repository,
// branch-ref, Git administrative record, VM or account deletion rights.
type ordinaryWorktreeCleanupV1 struct {
	purge     *purgeOwnerV1
	operation *SessionOperation
	stopped   *StoppedOrdinarySessionV1
	origin    worktreeOriginV1
	root      *os.Root
	parent    *os.Root
	leaf      string
	mount     uint64
	nodes     int
	capture   string
	manifest  Manifest
	started   bool
	closed    bool
	closeErr  error
}

func (purge *purgeOwnerV1) removeOrdinaryWorktreeV1(operation *SessionOperation, stopped *StoppedOrdinarySessionV1, manifest *Manifest) error {
	return purge.removeOrdinaryWorktreeWithProofV2(operation, stopped, manifest, nil)
}

func (purge *purgeOwnerV1) removeOrdinaryWorktreeWithProofV2(operation *SessionOperation, stopped *StoppedOrdinarySessionV1, manifest *Manifest, proof *purgeOrdinaryWorkLossV2) error {
	if purge == nil || purge.ctx == nil || stopped == nil || manifest == nil {
		return sessionLeaseError("worktree cleanup has no original native stop and owning transaction")
	}
	transaction, _ := purge.ctx.Value(purgeActualTransactionKeyV2{}).(*purgeActiveTransactionV2)
	if transaction == nil || transaction.owner != purge {
		return sessionLeaseError("worktree cleanup requires its same retained original purge transaction; preserve the worktree and resume that owner")
	}
	for _, subject := range transaction.subjects {
		if subject.operation == operation && subject.stopped == stopped && subject.manifest != nil && subject.manifest.ID == manifest.ID {
			if subject.worktree == nil {
				return nil
			} // Explicitly preserved during native capture.
			return subject.worktree.Continue(purge.ctx, stopped, proof)
		}
	}
	return sessionLeaseError("the retained worktree is not part of this actual transaction's original vector")
}

func captureOriginalPurgeWorktreeV3(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, manifest *Manifest) (result *ordinaryWorktreeCleanupV1, returned error) {
	if ctx == nil || purge == nil || operation == nil || operation.native == nil || !operation.Valid(operation.Primary()) || manifest == nil || manifest.Git == nil || manifest.Wall != nil || !ordinaryManifestV1(manifest) {
		return nil, sessionLeaseError("worktree capture has no original ordinary operation and protected manifest")
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if current.ID != manifest.ID || !reflect.DeepEqual(current.Git, manifest.Git) {
		return nil, sessionLeaseError("the original worktree description changed before native capture")
	}
	var origin worktreeOriginV1
	var found bool
	if err := purge.gate(func(control *placementControl, _ *os.Root) error {
		if _, err := purge.checkRoot("data", control); err != nil {
			return err
		}
		origin, found = control.WorktreeOrigins[manifest.ID]
		return nil
	}); err != nil {
		return nil, err
	}
	git := manifest.Git
	path := filepath.Join(WorktreesDir(), origin.Relative)
	if !found || len(git.extra) != 0 || validateWorktreeOriginsV1(map[string]worktreeOriginV1{manifest.ID: origin}) != nil || origin.Phase != "installed" || path != git.Worktree || origin.Repository != git.RepoRoot || origin.Branch != git.Branch || origin.BaseSHA != git.BaseSHA || !origin.Birth.knownV2() {
		purge.reportItem(false, "worktree", git.Worktree, "preserved: no matching original native filesystem generation receipt; save the worktree and inspect its original creation before removing it by hand")
		for index := range purge.report.Left {
			item := &purge.report.Left[index]
			if item.Kind == "worktree" && item.Path == git.Worktree {
				item.Hint = "Save this worktree and check its original creation record and filesystem support before removing it by hand. Force does not establish ownership."
			}
		}
		return nil, nil
	}
	owner := &ordinaryWorktreeCleanupV1{purge: purge, operation: operation, origin: origin, leaf: filepath.Base(path), capture: ".purge-worktree-" + purge.args.Nonce + "-" + manifest.ID, manifest: CloneManifest(*manifest)}
	complete := false
	defer func() {
		if !complete {
			closeErr := owner.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = owner
			}
		}
	}()
	owner.parent, err = cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	if err := origin.Parent.matchesRoot(owner.parent); err != nil {
		return nil, err
	}
	owner.root, err = cachefs.OpenPrivateChild(owner.parent, owner.leaf, false)
	if err != nil {
		return nil, err
	}
	if err := errors.Join(origin.Directory.matchesRoot(owner.root), checkWorktreeBirthV2(owner.root, origin.Birth)); err != nil {
		return nil, err
	}
	directory, err := owner.root.Open(".")
	if err != nil {
		return nil, err
	}
	owner.mount, err = stoppedCleanupMountV1(directory)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	complete = true
	return owner, nil
}

func (owner *ordinaryWorktreeCleanupV1) Continue(ctx context.Context, stopped *StoppedOrdinarySessionV1, proof *purgeOrdinaryWorkLossV2) error {
	if owner == nil || owner.closed || ctx == nil || stopped == nil || stopped.origin != owner.purge.placement || stopped.operation != owner.operation || owner.stopped != nil && owner.stopped != stopped {
		return sessionLeaseError("the worktree cleanup lost its SAME original physical subject")
	}
	if err := stopped.Check(ctx, owner.operation); err != nil {
		return err
	}
	if !owner.purge.args.Force {
		if err := proof.checkStoppedV2(ctx, owner.purge, owner.operation, stopped); err != nil {
			return err
		}
	}
	owner.stopped = stopped
	if err := owner.check(); err != nil {
		return err
	}
	unlinked, err := owner.originalDirectoryUnlinkedV3()
	if err != nil {
		return err
	}
	if unlinked {
		if err := syncCapturedDirectory(owner.parent); err != nil {
			return err
		}
		owner.reportCompleteV3()
		return nil
	}
	if err := owner.purge.gate(func(control *placementControl, _ *os.Root) error {
		plan, exists := control.Sessions[owner.operation.Primary()]
		if !exists || !plan.Stopped || !samePurgeDirectoryRef(plan.Directory, refOfSessionRootV1(owner.operation)) {
			return sessionLeaseError("the retained worktree lost its original stopped session plan")
		}
		relative := filepath.Join("worktrees", filepath.Dir(owner.origin.Relative), owner.capture)
		if len(plan.WorktreeCapture) != 0 && (string(plan.WorktreeCapture) != relative || !samePurgeDirectoryRef(plan.Worktree, owner.origin.Directory)) {
			return sessionLeaseError("the retained worktree plan changed its original binding")
		}
		plan.Worktree, plan.WorktreeCapture, plan.GitTip = owner.origin.Directory, []byte(relative), owner.origin.BaseSHA
		control.Sessions[owner.operation.Primary()] = plan
		return nil
	}); err != nil {
		return err
	}
	owner.started = true
	if owner.leaf != owner.capture {
		if err := cachefs.RenameOwnedDirectoryNoReplace(owner.parent, owner.leaf, owner.capture); err != nil {
			return err
		}
		// Retain the actual root across all following errors and retries.
		owner.leaf = owner.capture
		if err := errors.Join(owner.check(), syncCapturedDirectory(owner.parent)); err != nil {
			return err
		}
	}
	owner.nodes = 0
	if err := owner.removeChildren(owner.root, 0); err != nil {
		return err
	}
	if err := owner.check(); err != nil {
		return err
	}
	if err := owner.parent.Remove(owner.leaf); err != nil {
		return err
	}
	if err := syncCapturedDirectory(owner.parent); err != nil {
		return err
	}
	owner.reportCompleteV3()
	return nil
}

func (owner *ordinaryWorktreeCleanupV1) reportCompleteV3() {
	owner.purge.reportItem(true, "worktree", owner.manifest.Git.Worktree, "")
	owner.purge.reportItem(false, "branch", owner.origin.Branch, "the branch and Git administrative records need their separate original lineage cleanup")
}

func (owner *ordinaryWorktreeCleanupV1) originalDirectoryUnlinkedV3() (bool, error) {
	file, err := owner.root.Open(".")
	if err != nil {
		return false, err
	}
	info, err := file.Stat()
	closed := file.Close()
	_, _, _, links, known := footprintIdentity(info)
	if err != nil || closed != nil || !known || !owner.origin.Directory.matches(info) {
		return false, errors.Join(sessionLeaseError("the retained original worktree has unknown native identity"), err, closed)
	}
	return links == 0, nil
}

func (owner *ordinaryWorktreeCleanupV1) CheckBeforeStop(ctx context.Context) error {
	if owner == nil || owner.closed || owner.started || owner.stopped != nil || ctx == nil || owner.root == nil || owner.parent == nil || owner.operation == nil || !owner.operation.Valid(owner.operation.Primary()) {
		return sessionLeaseError("the original active worktree capture ended or changed")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.origin.Parent.matchesRoot(owner.parent), owner.origin.Directory.matchesRoot(owner.root), checkWorktreeBirthV2(owner.root, owner.origin.Birth)); err != nil {
		return err
	}
	current, err := owner.parent.Lstat(owner.leaf)
	if err != nil || !owner.origin.Directory.matches(current) {
		return errors.Join(sessionLeaseError("the active original worktree lost its named generation before stop"), err)
	}
	return nil
}

func (owner *ordinaryWorktreeCleanupV1) CheckComplete(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.closed || !owner.started || owner.stopped == nil {
		return sessionLeaseError("the retained worktree cleanup has no original physical completion")
	}
	if err := errors.Join(ctx.Err(), owner.stopped.checkStoppedOrdinaryNativeFactsV1(ctx)); err != nil {
		return err
	}
	unlinked, err := owner.originalDirectoryUnlinkedV3()
	if err != nil || !unlinked {
		return errors.Join(sessionLeaseError("the SAME original worktree has not positively unlinked"), err)
	}
	return nil
}

func (owner *ordinaryWorktreeCleanupV1) Close() error {
	if owner == nil {
		return nil
	}
	if owner.closed {
		return owner.closeErr
	}
	if owner.started {
		unlinked, err := owner.originalDirectoryUnlinkedV3()
		if err != nil || !unlinked {
			return errors.Join(sessionLeaseError("the SAME worktree cleanup still owns its original directory"), err)
		}
	}
	for _, root := range []*os.Root{owner.root, owner.parent} {
		if root != nil {
			owner.closeErr = errors.Join(owner.closeErr, root.Close())
		}
	}
	owner.closed = true
	return owner.closeErr
}

func refOfSessionRootV1(operation *SessionOperation) purgeRef {
	if operation == nil || operation.native == nil || operation.native.root == nil {
		return purgeRef{}
	}
	ref, err := nativeWorktreeDirectoryRefV1(operation.native.root)
	if err != nil {
		return purgeRef{}
	}
	return ref
}

func (owner *ordinaryWorktreeCleanupV1) check() error {
	if owner == nil || owner.purge == nil || owner.operation == nil || owner.stopped == nil || owner.root == nil || owner.parent == nil || owner.stopped.origin != owner.purge.placement {
		return sessionLeaseError("the original ordinary worktree cleanup owner ended")
	}
	if err := owner.stopped.checkStoppedOrdinaryNativeFactsV1(owner.purge.ctx); err != nil {
		return err
	}
	if err := errors.Join(owner.origin.Parent.matchesRoot(owner.parent), owner.origin.Directory.matchesRoot(owner.root)); err != nil {
		return err
	}
	unlinked, err := owner.originalDirectoryUnlinkedV3()
	if err != nil {
		return err
	}
	if unlinked {
		return nil
	}
	if err := checkWorktreeBirthV2(owner.root, owner.origin.Birth); err != nil {
		return err
	}
	current, err := owner.parent.Lstat(owner.leaf)
	if err != nil || !owner.origin.Directory.matches(current) {
		return sessionLeaseError("the retained original worktree lost its named binding")
	}

	data := owner.purge.roots["data"]
	if data == nil {
		return sessionLeaseError("the original worktree data namespace ended")
	}
	named, err := cachefs.OpenPrivateRoot(paths.DataDir(), false)
	if err != nil {
		return err
	}
	checked := data.ref.matchesRoot(named)
	closed := named.Close()
	return errors.Join(checked, closed, data.ref.matchesRoot(data.root))
}

func (owner *ordinaryWorktreeCleanupV1) removeChildren(parent *os.Root, depth int) error {
	if depth > 128 || owner.nodes >= 100000 {
		return sessionLeaseError("the original worktree exceeds its cleanup inspection bound")
	}
	if err := owner.check(); err != nil {
		return err
	}
	directory, err := parent.Open(".")
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(100001 - owner.nodes)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if len(entries)+owner.nodes > 100000 {
		return sessionLeaseError("the original worktree exceeds its cleanup entry bound")
	}
	for _, entry := range entries {
		owner.nodes++
		if err := owner.removeChild(parent, entry.Name(), depth); err != nil {
			return err
		}
	}
	return nil
}

func (owner *ordinaryWorktreeCleanupV1) removeChild(parent *os.Root, leaf string, depth int) error {
	if err := owner.check(); err != nil {
		return err
	}
	info, err := parent.Lstat(leaf)
	if err != nil {
		return err
	}
	ref := refOf(info)
	if ref.UID != uint64(os.Getuid()) || info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSymlink == 0 {
		return sessionLeaseError("preserved a foreign or writable worktree resource")
	}
	capture := ".purge-worktree-node-" + owner.purge.args.Nonce
	if info.IsDir() {
		child, err := parent.OpenRoot(leaf)
		if err != nil {
			return err
		}
		defer child.Close()
		directory, err := child.Open(".")
		if err != nil {
			return err
		}
		mount, err := stoppedCleanupMountV1(directory)
		closeErr := directory.Close()
		if err != nil || closeErr != nil || mount != owner.mount || ref.matchesRoot(child) != nil {
			return errors.Join(sessionLeaseError("preserved a changed or mounted worktree directory"), err, closeErr)
		}
		if err := owner.removeChildren(child, depth+1); err != nil {
			return err
		}
		if err := cachefs.RenameOwnedDirectoryNoReplace(parent, leaf, capture); err != nil {
			return err
		}
		current, err := parent.Lstat(capture)
		if err != nil || !ref.matches(current) {
			return errors.Join(sessionLeaseError("preserved a replaced captured worktree directory"), cachefs.RenameOwnedDirectoryNoReplace(parent, capture, leaf))
		}
		if err := errors.Join(owner.check(), ref.matchesRoot(child)); err != nil {
			return err
		}
		if err := parent.Remove(capture); err != nil {
			return err
		}
		return syncCapturedDirectory(parent)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := parent.Readlink(leaf)
		if err != nil {
			return err
		}
		if err := cachefs.RenameOwnedSymlinkNoReplace(parent, leaf, capture); err != nil {
			return err
		}
		current, err := parent.Lstat(capture)
		currentTarget, readErr := parent.Readlink(capture)
		if err != nil || readErr != nil || !ref.matches(current) || currentTarget != target {
			return errors.Join(sessionLeaseError("preserved a changed captured worktree link"), cachefs.RenameOwnedSymlinkNoReplace(parent, capture, leaf))
		}
		if err := owner.check(); err != nil {
			return err
		}
		if err := parent.Remove(capture); err != nil {
			return err
		}
		return syncCapturedDirectory(parent)
	}
	if !info.Mode().IsRegular() {
		return sessionLeaseError("preserved an unknown worktree resource type")
	}
	return removeCapturedOwnedFileV1(parent, leaf, ref, capture, func(string) error { return owner.check() }, func(file *os.File) error {
		mount, err := stoppedCleanupMountV1(file)
		if err != nil || mount != owner.mount {
			return errors.Join(sessionLeaseError("preserved a mounted worktree file"), err)
		}
		return owner.check()
	})
}
