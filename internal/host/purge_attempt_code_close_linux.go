//go:build linux

package host

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// Close refuses while an actual bootstrap loan remains with an execution
// owner. It removes only this creator's exact code/lock and empty directory.
// Partial cleanup retains these real descriptors for this same object's retry.
func (owner *purgeAttemptCodeV2) Close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return errors.Join(owner.closeErr, owner.checkRetiredLockedV4())
	}
	owner.closing = true // A close request cannot admit any future launch.
	if len(owner.loans) != 0 {
		return sessionLeaseError("the original purge code still has an actual native execution loan")
	}
	if owner.directoryCreated && (owner.directory == nil || !owner.directoryRef.validDirectory()) {
		return errors.Join(placementError("the original code directory creation has no retained native identity; preserve its owner"),
			owner.retirement.closeFilesV4([]*os.File{owner.copyWriter, owner.copy, owner.lease, owner.source}),
			owner.retirement.closeRootsV4([]*os.Root{owner.directory, owner.home}))
	}
	if owner.directory != nil {
		unlinked, err := owner.originalCodeDirectoryUnlinkedV2()
		if err != nil {
			return err
		}
		if !unlinked {
			if err := errors.Join(owner.homeRef.matchesRoot(owner.home), owner.directoryRef.matchesRoot(owner.directory)); err != nil {
				return err
			}
			current, err := owner.home.Lstat(owner.directoryLeaf)
			if err != nil || !owner.directoryRef.matches(current) {
				return errors.Join(sessionLeaseError("the original temporary code directory changed before cleanup"), err)
			}
			if err := owner.checkOwnCodeEntriesV2(); err != nil {
				return err
			}
			if err := removeOwnPurgeCodeFileV2(owner.directory, owner.copy, owner.copyInfo, owner.copyCreated, &owner.copyLeaf, ".remove-code"); err != nil {
				return err
			}
			if err := removeOwnPurgeCodeFileV2(owner.directory, owner.lease, owner.leaseInfo, owner.leaseCreated, &owner.leaseLeaf, ".remove-lock"); err != nil {
				return err
			}
			if err := owner.checkEmptyCodeDirectoryV2(); err != nil {
				return err
			}
			if owner.directoryLeaf != owner.directoryCapture {
				if err := cachefs.RenameOwnedDirectoryNoReplace(owner.home, owner.directoryLeaf, owner.directoryCapture); err != nil {
					return err
				}
				owner.directoryLeaf = owner.directoryCapture
			}
			current, err = owner.home.Lstat(owner.directoryLeaf)
			if err != nil || !owner.directoryRef.matches(current) {
				return errors.Join(sessionLeaseError("preserved a replaced temporary code directory capture"), err)
			}
			if err := owner.home.Remove(owner.directoryLeaf); err != nil {
				return err
			}
		}
		if err := syncCapturedDirectory(owner.home); err != nil {
			return err
		}
	}
	owner.closeErr = errors.Join(owner.closeErr,
		owner.retirement.closeFilesV4([]*os.File{owner.copyWriter, owner.copy, owner.lease, owner.source}),
		owner.retirement.closeRootsV4([]*os.Root{owner.directory, owner.home}))
	native := owner.checkRetiredLockedV4()
	if native == nil {
		owner.closed = true
	}
	return errors.Join(owner.closeErr, native)
}

func (owner *purgeAttemptCodeV2) originalCodeDirectoryUnlinkedV2() (bool, error) {
	file, err := owner.directory.Open(".")
	if err != nil {
		return false, err
	}
	info, err := file.Stat()
	closed := owner.retirement.closeFilesV4([]*os.File{file})
	if err != nil || closed != nil || !owner.directoryRef.matches(info) {
		return false, errors.Join(sessionLeaseError("the retained original code directory descriptor changed"), err, closed)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return false, sessionLeaseError("the original code directory has unknown native link state")
	}
	return links == 0, nil
}

func (owner *purgeAttemptCodeV2) checkOwnCodeEntriesV2() error {
	file, err := owner.directory.Open(".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(3)
	closed := owner.retirement.closeFilesV4([]*os.File{file})
	if err != nil && !errors.Is(err, io.EOF) || closed != nil {
		return errors.Join(err, closed)
	}
	for _, entry := range entries {
		if entry.Name() != owner.copyLeaf && entry.Name() != owner.leaseLeaf {
			return sessionLeaseError("the temporary code directory contains an unrelated preserved entry")
		}
	}
	return nil
}

func (owner *purgeAttemptCodeV2) checkEmptyCodeDirectoryV2() error {
	file, err := owner.directory.Open(".")
	if err != nil {
		return err
	}
	entries, err := file.ReadDir(1)
	closed := owner.retirement.closeFilesV4([]*os.File{file})
	if err != nil && !errors.Is(err, io.EOF) || closed != nil {
		return errors.Join(err, closed)
	}
	if len(entries) != 0 {
		return sessionLeaseError("the original temporary code directory is not empty")
	}
	return owner.directoryRef.matchesRoot(owner.directory)
}

func removeOwnPurgeCodeFileV2(root *os.Root, file *os.File, created os.FileInfo, reference cachefs.Reference, leaf *string, capture string) error {
	if file == nil {
		return nil
	}
	info, err := file.Stat()
	if err != nil || created == nil || !os.SameFile(info, created) {
		return errors.Join(sessionLeaseError("the actual temporary code creator descriptor changed"), err)
	}
	_, _, _, links, known := footprintIdentity(info)
	if known && links == 0 {
		return nil
	} // This same retained creation is already unlinked; no named adoption.
	mode := info.Mode().Perm()
	if err := reference.CheckCreatedFile(root, *leaf, file, mode); err != nil {
		return err
	}
	if *leaf != capture {
		if err := cachefs.RenameOwnedFileNoReplace(root, file, *leaf, capture, mode); err != nil {
			return err
		}
		*leaf = capture
	}
	if err := reference.CheckCreatedFile(root, *leaf, file, mode); err != nil {
		return err
	}
	if err := root.Remove(*leaf); err != nil {
		return err
	}
	return syncCapturedDirectory(root)
}

// The same factory retains every transient scan and writable handoff fact.
// Directory unlink still comes only from its actual original creation above.
func (owner *purgeAttemptCodeV2) checkRetiredLockedV4() error {
	if len(owner.loans) != 0 {
		return placementError("the original code factory still has actual native loans")
	}
	return errors.Join(owner.retirement.checkFilesV4([]*os.File{owner.copyWriter, owner.copy, owner.lease, owner.source}), owner.retirement.checkRootsV4([]*os.Root{owner.directory, owner.home}), owner.retirement.checkAllRetiredV4())
}

func (owner *purgeAttemptCodeV2) checkNativeRetiredV3(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return placementError("the original code factory retirement is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.closed {
		return placementError("the actual original code factory has not physically retired")
	}
	return owner.checkRetiredLockedV4()
}

func (owner *purgeAttemptCodeV2) CloseDiagnosticsV3() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return errors.Join(owner.closeErr, owner.retirement.diagnosticsV4())
}
