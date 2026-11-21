//go:build linux

package host

import (
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"golang.org/x/sys/unix"
	"os"
)

func openNoneColdHolderV1(ctx context.Context, operation *SessionOperation, source *Manifest) (result *sessionFileLease, returned error) {
	if ctx == nil || operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || source == nil {
		return nil, sessionLeaseError("the None holder has no original owning OP")
	}
	root, err := operation.native.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	var file *os.File
	defer func() {
		if result == nil {
			if file != nil {
				returned = errors.Join(returned, file.Close())
			}
			returned = errors.Join(returned, root.Close())
		}
	}()
	rootRef, err := cachefs.CaptureRoot(root)
	if err != nil {
		return nil, err
	}
	file, err = cachefs.OpenFile(root, holderLockFile, os.O_RDWR, 0600) // NEVER O_CREATE.
	if err != nil {
		return nil, err
	}
	held, err := cachefs.TryLock(file, cachefs.Exclusive)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, UserError("busy", "the original None holder still owns its native lifetime", "Wait for its original stop to complete before preparing a resume.")
	}
	ref, err := cachefs.CaptureFile(root, holderLockFile, file, 0600)
	if err != nil {
		return nil, err
	}
	lease := &sessionFileLease{root: root, file: file, rootRef: rootRef, fileRef: ref, id: operation.Primary(), path: paths.SessionDir(operation.Primary()), name: holderLockFile}
	if err := errors.Join(operation.native.rootRef.CheckRoot(root), lease.check(ctx)); err != nil {
		return nil, err
	}
	if source.Run.BootID == BootID() {
		if !validNoneCleanupV1(source, BootID()) {
			return nil, sessionLeaseError("the same-boot None holder has no original cleanup DATA")
		}
		if err := errors.Join(source.NoneCleanup.Directory.CheckRoot(root), source.NoneCleanup.Holder.CheckFile(root, holderLockFile, file, 0600)); err != nil {
			return nil, errors.Join(sessionLeaseError("the original None cleanup directory or holder inode was replaced"), err)
		}
	}
	return lease, nil
}

func duplicateNoneColdHolderV1(ctx context.Context, cold *NoneColdResumeV1) (*os.File, error) {
	lease := cold.holder
	if lease == nil {
		return nil, sessionLeaseError("the original None holder is absent")
	}
	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.borrowed || lease.name != holderLockFile || lease.id != cold.operation.Primary() {
		return nil, sessionLeaseError("the original None holder has another native role")
	}
	if err := lease.checkLocked(ctx); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(lease.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "same-original-none-holder"), nil
}
