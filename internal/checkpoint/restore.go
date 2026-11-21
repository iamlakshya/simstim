package checkpoint

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// Restore replaces the disk with a clone of the pinned original selection.
// The caller must first stop and physically join the SAME native runtime,
// then CreateStopped a safety checkpoint with keep == 0. checkStopped must
// prove that original closure before and after replacement; nil is refused.
// No old checkpoint is deleted here. After a committed rename, the returned
// file is the store creator's NEW disk descriptor, even if a later check
// fails. The native owner must retain/adopt it and close its old descriptor.
func (s *Store) Restore(ctx context.Context, selection *Selection, checkStopped func(context.Context) error) (_ *os.File, returned error) {
	if s == nil || selection == nil || checkStopped == nil {
		return nil, failure("rewind has no original selection and stopped runtime owner", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.noPreparedRestoreV1(); err != nil {
		return nil, err
	}
	selection.mu.Lock()
	defer selection.mu.Unlock()
	if err := errors.Join(s.checkOwned(ctx, checkStopped), selection.check(s)); err != nil {
		return nil, err
	}
	if s.sequence <= selection.sequence {
		return nil, failure("rewind must checkpoint the current disk before replacing it", nil)
	}
	if err := s.safetySource.CheckFile(s.diskRoot, diskName, s.disk, 0o600); err != nil {
		return nil, failure("the current disk changed after the safety checkpoint", err)
	}
	part, err := createPart(s.diskRoot)
	if err != nil {
		return nil, failure("cannot create the rewind replacement", err)
	}
	committed := false
	defer func() {
		if !committed {
			returned = errors.Join(returned, part.remove())
		}
	}()
	if err := s.clone(part.file, selection.disk); err != nil {
		return nil, cloneFailure("cannot clone the selected rewind disk", err)
	}
	if err := errors.Join(ctx.Err(), part.file.Sync(), selection.check(s), s.checkOwned(ctx, checkStopped), s.safetySource.CheckFile(s.diskRoot, diskName, s.disk, 0o600), part.ref.CheckCreatedFile(s.diskRoot, part.name, part.file, 0o600)); err != nil {
		return nil, failure("cannot finish the rewind replacement clone", err)
	}
	if err := s.diskRoot.Rename(part.name, diskName); err != nil {
		return nil, failure("cannot replace the original stopped session disk", err)
	}
	part.name = diskName
	committed = true
	// Keep the actual new descriptor even on any post-rename error. The old
	// original remains its caller's responsibility and is never closed here.
	s.disk = part.file
	s.diskRef = part.ref
	if err := errors.Join(part.ref.CheckCreatedFile(s.diskRoot, diskName, part.file, 0o600), syncRoot(s.diskRoot), s.checkOwned(ctx, checkStopped), selection.check(s)); err != nil {
		return part.file, failure("the rewind disk was replaced but its owner must reconcile the final check", err)
	}
	s.safetySource = cachefs.Reference{}
	return part.file, nil
}
