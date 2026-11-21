package checkpoint

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// Probe performs a real FICLONE between fresh private files on the original
// disk and checkpoint roots. A filesystem name never enables checkpoints.
func (s *Store) Probe(ctx context.Context) (returned error) {
	if s == nil {
		return failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return err
	}
	source, err := createPart(s.diskRoot)
	if err != nil {
		return failure("cannot create the checkpoint clone probe", err)
	}
	defer func() { returned = errors.Join(returned, source.remove()) }()
	target, err := createPart(s.entries)
	if err != nil {
		return failure("cannot create the checkpoint clone probe target", err)
	}
	defer func() { returned = errors.Join(returned, target.remove()) }()
	const contents = "disk checkpoint clone probe\n"
	if _, err := source.file.WriteString(contents); err != nil {
		return failure("cannot write the checkpoint clone probe", err)
	}
	if err := source.file.Sync(); err != nil {
		return failure("cannot sync the checkpoint clone probe", err)
	}
	if err := s.clone(target.file, source.file); err != nil {
		return cloneFailure("the checkpoint clone probe failed", err)
	}
	data := make([]byte, len(contents))
	if _, err := target.file.ReadAt(data, 0); err != nil || string(data) != contents {
		return failure("the checkpoint clone probe did not preserve its bytes", err)
	}
	return s.checkOwned(ctx, s.check)
}

// Create clones the current disk after its caller has frozen the original
// guest. It never falls back to a full copy. keep == 0 postpones retention so
// a rewind can preserve every old checkpoint until replacement has committed.
func (s *Store) Create(ctx context.Context, at time.Time, metadata Metadata, keep int) (Entry, error) {
	if s == nil {
		return Entry{}, failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.create(ctx, at, metadata, keep, s.check)
}

// CreateStopped makes the pre-rewind safety checkpoint after the SAME native
// owner has proved that its original runtime physically stopped. A saved
// status or the running-source callback cannot substitute for checkStopped.
func (s *Store) CreateStopped(ctx context.Context, at time.Time, metadata Metadata, keep int, checkStopped func(context.Context) error) (Entry, error) {
	if s == nil || checkStopped == nil || keep != 0 {
		return Entry{}, failure("the safety checkpoint has no original stopped owner", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.create(ctx, at, metadata, keep, checkStopped)
}

func (s *Store) create(ctx context.Context, at time.Time, metadata Metadata, keep int, owner func(context.Context) error) (_ Entry, returned error) {
	if err := s.noPreparedRestoreV1(); err != nil {
		return Entry{}, err
	}
	if keep < 0 || keep > 10000 || at.IsZero() {
		return Entry{}, failure("the checkpoint time or retention count is invalid", nil)
	}
	if err := s.checkOwned(ctx, owner); err != nil {
		return Entry{}, err
	}
	data, err := metadataBytes(metadata)
	if err != nil {
		return Entry{}, failure("cannot encode checkpoint metadata", err)
	}
	entry := Entry{Time: at.UTC().Truncate(time.Millisecond), Metadata: cloneMetadata(metadata)}
	key := entry.Time.Format(TimeLayout)
	if _, err := s.entries.Lstat(key + ".disk"); !errors.Is(err, os.ErrNotExist) {
		return Entry{}, failure("the checkpoint time already exists or cannot be inspected", err)
	}
	if _, err := s.entries.Lstat(key + ".json"); !errors.Is(err, os.ErrNotExist) {
		return Entry{}, failure("the checkpoint metadata time already exists or cannot be inspected", err)
	}
	disk, err := createPart(s.entries)
	if err != nil {
		return Entry{}, failure("cannot create the checkpoint disk", err)
	}
	metadataFile, err := createPart(s.entries)
	if err != nil {
		return Entry{}, errors.Join(failure("cannot create checkpoint metadata", err), disk.remove())
	}
	published := false
	defer func() {
		if !published {
			returned = errors.Join(returned, disk.remove(), metadataFile.remove())
		} else {
			returned = errors.Join(returned, disk.file.Close(), metadataFile.file.Close())
		}
	}()
	before, err := cachefs.ValidateDescriptor(s.disk, 0o600)
	if err != nil {
		return Entry{}, failure("cannot identify the frozen original disk", err)
	}
	if err := s.clone(disk.file, s.disk); err != nil {
		return Entry{}, cloneFailure("cannot clone the checkpoint disk", err)
	}
	if err := errors.Join(unchanged(s.disk, before), ctx.Err(), disk.file.Sync(), s.checkOwned(ctx, owner)); err != nil {
		return Entry{}, failure("cannot finish the checkpoint disk clone", err)
	}
	if _, err := metadataFile.file.Write(data); err != nil {
		return Entry{}, failure("cannot write checkpoint metadata", err)
	}
	if err := errors.Join(metadataFile.file.Sync(), s.checkOwned(ctx, owner)); err != nil {
		return Entry{}, failure("cannot finish checkpoint metadata", err)
	}
	if err := disk.publish(key + ".disk"); err != nil {
		return Entry{}, failure("cannot publish the checkpoint disk", err)
	}
	if err := metadataFile.publish(key + ".json"); err != nil {
		return Entry{}, failure("cannot publish checkpoint metadata", err)
	}
	if err := errors.Join(syncRoot(s.entries), s.checkOwned(ctx, owner)); err != nil {
		return Entry{}, failure("cannot commit the checkpoint", err)
	}
	s.safetySource, err = cachefs.CaptureFile(s.diskRoot, diskName, s.disk, 0o600)
	if err != nil || s.sequence == ^uint64(0) {
		return Entry{}, failure("cannot retain the safety checkpoint's original disk", err)
	}
	s.sequence++
	s.createdDisk, err = cachefs.CaptureFile(s.entries, key+".disk", disk.file, 0o600)
	if err != nil {
		return Entry{}, failure("cannot retain the new checkpoint creator", err)
	}
	s.createdMetadata, err = cachefs.CaptureFile(s.entries, key+".json", metadataFile.file, 0o600)
	if err != nil {
		return Entry{}, failure("cannot retain the new checkpoint metadata creator", err)
	}
	s.createdKey = key
	published = true
	if keep > 0 {
		if err := s.trim(ctx, keep, owner); err != nil {
			return entry, err // The complete new checkpoint remains available.
		}
	}
	return entry, nil
}

func unchanged(file *os.File, before os.FileInfo) error {
	after, err := cachefs.ValidateDescriptor(file, 0o600)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return failure("the disk changed while it was copied", nil)
	}
	return nil
}
