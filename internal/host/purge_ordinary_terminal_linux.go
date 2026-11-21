//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This consumer retains the actual original terminal descriptors across every
// unlink and retry. It never reopens a deleted OP, holder or manifest by name.
type purgeOrdinaryTerminalV2 struct {
	mu        sync.Mutex
	purge     *purgeOwnerV1
	operation *SessionOperation
	stopped   *StoppedOrdinarySessionV1
	root      *os.Root
	parent    *os.Root
	parentRef cachefs.Reference
	directory purgeRef
	leaf      string
	capture   string
	files     []*purgeTerminalFileV2
	started   bool
	ready     bool
	closed    bool
	closeErr  error
}

type purgeTerminalFileV2 struct {
	file      *os.File
	info      os.FileInfo
	reference cachefs.Reference
	leaf      string
	capture   string
	owned     bool
}

func newPurgeOrdinaryTerminalV2(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, stopped *StoppedOrdinarySessionV1) (result *purgeOrdinaryTerminalV2, returned error) {
	if ctx == nil || purge == nil || operation == nil || operation.native == nil || stopped == nil || stopped.holder == nil || stopped.origin != purge.placement {
		return nil, sessionLeaseError("ordinary terminal cleanup has no original retained operation and physical source")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return nil, err
	}
	owner := &purgeOrdinaryTerminalV2{purge: purge, operation: operation, stopped: stopped, root: operation.native.root, leaf: operation.Primary(), capture: ".purge-session-" + purge.args.Nonce + "-" + operation.Primary()}
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
	var err error
	owner.parent, err = cachefs.OpenPrivateRoot(filepath.Dir(operation.native.path), false)
	if err != nil {
		return nil, err
	}
	owner.parentRef, err = cachefs.CaptureRoot(owner.parent)
	if err != nil {
		return nil, err
	}
	owner.directory, err = nativeWorktreeDirectoryRefV1(owner.root)
	if err != nil {
		return nil, err
	}
	current, err := owner.parent.Lstat(owner.leaf)
	if err != nil || !owner.directory.matches(current) {
		return nil, errors.Join(sessionLeaseError("the original stopped directory changed before terminal capture"), err)
	}
	first, err := cachefs.OpenFile(owner.root, firstLockFile, os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := owner.retainFileV2(firstLockFile, first, true); err != nil {
		return nil, err
	}
	held, err := cachefs.TryLock(first, cachefs.Exclusive)
	if err != nil || !held {
		return nil, errors.Join(sessionLeaseError("the original first-save owner has not actually settled"), err)
	}
	manifest, err := cachefs.OpenFile(owner.root, "manifest.json", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := owner.retainFileV2("manifest.json", manifest, true); err != nil {
		return nil, err
	}
	// Remove the protected manifest first. All later checks use the retained
	// genuine native stopped object, never a reload or recreated lease.
	owner.files[0], owner.files[1] = owner.files[1], owner.files[0]
	if err := owner.retainFileV2(holderLockFile, stopped.holder.file, false); err != nil {
		return nil, err
	}
	if err := owner.retainFileV2(lockFile, operation.native.file, false); err != nil {
		return nil, err
	}
	if err := owner.checkNamesV2(); err != nil {
		return nil, err
	}
	owner.ready = true
	complete = true
	return owner, nil
}

func (owner *purgeOrdinaryTerminalV2) retainFileV2(leaf string, file *os.File, owned bool) error {
	entry := &purgeTerminalFileV2{file: file, leaf: leaf, capture: ".purge-terminal-" + owner.purge.args.Nonce + "-" + leaf, owned: owned}
	owner.files = append(owner.files, entry) // Cleanup custody exists before validation can fail.
	var err error
	entry.info, err = cachefs.ValidateFile(owner.root, leaf, file, 0o600)
	if err != nil {
		return err
	}
	if leaf != "manifest.json" && entry.info.Size() != 0 {
		return sessionLeaseError("the original terminal lease has unknown payload")
	}
	entry.reference, err = cachefs.CaptureFile(owner.root, leaf, file, 0o600)
	return err
}

func (owner *purgeOrdinaryTerminalV2) checkNativeV2(ctx context.Context) error {
	if owner == nil || ctx == nil || owner.closed || !owner.ready || len(owner.files) != 4 || owner.purge == nil || owner.operation == nil || owner.operation.native == nil || owner.stopped == nil || owner.root != owner.operation.native.root || owner.stopped.operation != owner.operation || owner.stopped.origin != owner.purge.placement {
		return sessionLeaseError("the original ordinary terminal cleanup ended or changed")
	}
	if err := errors.Join(ctx.Err(), owner.stopped.checkStoppedOrdinaryNativeFactsV1(ctx), owner.operation.native.rootRef.CheckRoot(owner.root), owner.parentRef.CheckRoot(owner.parent), owner.directory.matchesRoot(owner.root)); err != nil {
		return err
	}
	file, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil || !owner.directory.matches(info) {
		return errors.Join(sessionLeaseError("the retained terminal root changed"), err, closed)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return sessionLeaseError("the terminal root has unknown link state")
	}
	if links == 0 {
		return nil
	}
	current, err := owner.parent.Lstat(owner.leaf)
	if err != nil || !owner.directory.matches(current) {
		return errors.Join(sessionLeaseError("the original terminal directory changed its parent's named binding"), err)
	}
	return nil
}

func (owner *purgeOrdinaryTerminalV2) checkNamesV2() error {
	file, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(9)
	closed := file.Close()
	if err != nil && !errors.Is(err, io.EOF) || closed != nil {
		return errors.Join(err, closed)
	}
	allowed := map[string]bool{}
	for _, entry := range owner.files {
		allowed[entry.leaf] = true
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return sessionLeaseError("the original terminal root contains an unsettled or unrelated resource")
		}
	}
	return nil
}

func terminalOriginalUnlinkedV2(file *os.File, expected os.FileInfo) (bool, error) {
	if file == nil || expected == nil {
		return false, sessionLeaseError("the original terminal description is absent")
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(info, expected) || info.Mode() != expected.Mode() || info.Size() != expected.Size() || !info.ModTime().Equal(expected.ModTime()) {
		return false, errors.Join(sessionLeaseError("the retained terminal creation changed its native object"), err)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return false, sessionLeaseError("the original terminal description has unknown native link state")
	}
	return links == 0, nil
}

func (owner *purgeOrdinaryTerminalV2) removeFileV2(ctx context.Context, entry *purgeTerminalFileV2) error {
	if err := owner.checkNativeV2(ctx); err != nil {
		return err
	}
	unlinked, err := terminalOriginalUnlinkedV2(entry.file, entry.info)
	if err != nil || unlinked {
		return err
	}
	if err := entry.reference.CheckFile(owner.root, entry.leaf, entry.file, 0o600); err != nil {
		return err
	}
	owner.started = true // Records actual attempted mutation; never creates unlink permission.
	if entry.leaf != entry.capture {
		previous := entry.leaf
		if err := cachefs.RenameOwnedFileNoReplace(owner.root, entry.file, previous, entry.capture, 0o600); err != nil {
			return err
		}
		if err := entry.reference.CheckFile(owner.root, entry.capture, entry.file, 0o600); err != nil {
			return errors.Join(err, cachefs.RenameNoReplace(owner.root, entry.capture, previous))
		}
		entry.leaf = entry.capture
	}
	if err := errors.Join(owner.checkNativeV2(ctx), entry.reference.CheckFile(owner.root, entry.leaf, entry.file, 0o600)); err != nil {
		return err
	}
	if err := owner.root.Remove(entry.leaf); err != nil {
		return err
	}
	return syncCapturedDirectory(owner.root)
}

// Continue retains this same original root and every original file through a
// partial capture/unlink. Kernel object facts distinguish done from unknown.
func (owner *purgeOrdinaryTerminalV2) Continue(ctx context.Context) error {
	if owner == nil {
		return sessionLeaseError("the original ordinary terminal consumer is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := owner.checkNativeV2(ctx); err != nil {
		return err
	}
	file, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil || !owner.directory.matches(info) {
		return errors.Join(sessionLeaseError("the original terminal root changed"), err, closed)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return sessionLeaseError("the original terminal root has unknown native link state")
	}
	if links == 0 {
		return syncCapturedDirectory(owner.parent)
	}
	if err := owner.checkNamesV2(); err != nil {
		return err
	}
	for _, entry := range owner.files {
		if err := owner.removeFileV2(ctx, entry); err != nil {
			return err
		}
	}
	if err := owner.checkNamesV2(); err != nil {
		return err
	}
	for _, entry := range owner.files {
		unlinked, err := terminalOriginalUnlinkedV2(entry.file, entry.info)
		if err != nil || !unlinked {
			return errors.Join(sessionLeaseError("an original terminal file remains linked"), err)
		}
	}
	if owner.leaf != owner.capture {
		previous := owner.leaf
		current, err := owner.parent.Lstat(previous)
		if err != nil || !owner.directory.matches(current) {
			return errors.Join(sessionLeaseError("the original session directory changed before its own capture"), err)
		}
		if err := cachefs.RenameOwnedDirectoryNoReplace(owner.parent, previous, owner.capture); err != nil {
			return err
		}
		current, err = owner.parent.Lstat(owner.capture)
		if err != nil || !owner.directory.matches(current) {
			return errors.Join(sessionLeaseError("preserved a replaced original session directory capture"), err, cachefs.RenameNoReplace(owner.parent, owner.capture, previous))
		}
		owner.leaf = owner.capture
	}
	if err := owner.checkNativeV2(ctx); err != nil {
		return err
	}
	if err := owner.parent.Remove(owner.leaf); err != nil {
		return err
	}
	return syncCapturedDirectory(owner.parent)
}

func (owner *purgeOrdinaryTerminalV2) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return owner.closeErr
	}
	if owner.started {
		if err := owner.checkCompleteLockedV2(context.Background()); err != nil {
			return err
		}
	}
	for _, entry := range owner.files {
		if entry.owned {
			owner.closeErr = errors.Join(owner.closeErr, entry.file.Close())
		}
	}
	if owner.parent != nil {
		owner.closeErr = errors.Join(owner.closeErr, owner.parent.Close())
	}
	owner.closed = true
	return owner.closeErr
}

// CheckComplete checks actual original unlinks, not a saved Cleaned field.
func (owner *purgeOrdinaryTerminalV2) CheckComplete(ctx context.Context) error {
	if owner == nil {
		return sessionLeaseError("the original terminal consumer is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.checkCompleteLockedV2(ctx)
}

func (owner *purgeOrdinaryTerminalV2) checkCompleteLockedV2(ctx context.Context) error {
	if err := owner.checkNativeV2(ctx); err != nil {
		return err
	}
	file, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil || !owner.directory.matches(info) {
		return errors.Join(sessionLeaseError("the original terminal directory changed"), err, closed)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known || links != 0 {
		return sessionLeaseError("the original terminal directory has not positively unlinked")
	}
	for _, entry := range owner.files {
		unlinked, err := terminalOriginalUnlinkedV2(entry.file, entry.info)
		if err != nil || !unlinked {
			return errors.Join(sessionLeaseError("an original terminal file remains linked"), err)
		}
	}
	return nil
}
