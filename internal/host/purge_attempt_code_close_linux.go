//go:build linux

package host

import (
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
		return nil
	}
	owner.closing = true // A close request cannot admit any future launch.
	if len(owner.loans) != 0 {
		return sessionLeaseError("the original purge code still has an actual native execution loan")
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
	var failure error
	for _, file := range []*os.File{owner.copy, owner.lease, owner.source} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	for _, root := range []*os.Root{owner.directory, owner.home} {
		if root != nil {
			failure = errors.Join(failure, root.Close())
		}
	}
	owner.closed = true
	return failure
}

func (owner *purgeAttemptCodeV2) originalCodeDirectoryUnlinkedV2() (bool, error) {
	file, err := owner.directory.Open(".")
	if err != nil {
		return false, err
	}
	info, err := file.Stat()
	closed := file.Close()
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
	closed := file.Close()
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
	closed := file.Close()
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
