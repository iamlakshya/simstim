package checkpoint

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// RemoveCreated rolls back only the last pair this SAME live Store created.
// It is used when freeze renewal fails after publication, before retention.
// An Entry from disk or another Store can never issue this deletion authority.
func (s *Store) RemoveCreated(ctx context.Context, entry Entry) (returned error) {
	if s == nil {
		return failure("the original checkpoint creator is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.noPreparedRestoreV1(); err != nil {
		return err
	}
	key := entry.Time.UTC().Format(TimeLayout)
	if s.createdKey == "" || key != s.createdKey {
		return failure("the checkpoint belongs to another creation", nil)
	}
	if err := s.checkOwned(ctx, s.check); err != nil {
		return err
	}
	disk, err := cachefs.OpenFile(s.entries, key+".disk", os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, disk.Close()) }()
	metadata, err := cachefs.OpenFile(s.entries, key+".json", os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, metadata.Close()) }()
	if err := errors.Join(s.createdDisk.CheckFile(s.entries, key+".disk", disk, 0o600), s.createdMetadata.CheckFile(s.entries, key+".json", metadata, 0o600), s.checkOwned(ctx, s.check)); err != nil {
		return err
	}
	if err := s.entries.Remove(key + ".json"); err != nil {
		return err
	}
	if err := s.createdDisk.CheckFile(s.entries, key+".disk", disk, 0o600); err != nil {
		return err
	}
	if err := s.entries.Remove(key + ".disk"); err != nil {
		return err
	}
	s.createdKey = ""
	s.safetySource = cachefs.Reference{}
	return errors.Join(syncRoot(s.entries), s.checkOwned(ctx, s.check))
}
