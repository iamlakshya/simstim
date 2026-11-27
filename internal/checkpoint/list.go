package checkpoint

import (
	"context"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Selection retains the SAME checkpoint disk and metadata selected before a
// rewind creates its safety checkpoint. It carries data, never stop authority.
type Selection struct {
	mu                   sync.Mutex
	store                *Store
	key                  string
	disk, metadata       *os.File
	diskRef, metadataRef cachefs.Reference
	entry                Entry
	closed               bool
	closeErr             error
	sequence             uint64
}

// Entry returns an independent copy of the selected checkpoint's metadata.
func (selection *Selection) Entry() Entry {
	if selection == nil {
		return Entry{}
	}
	selection.mu.Lock()
	defer selection.mu.Unlock()
	return Entry{Time: selection.entry.Time, Metadata: cloneMetadata(selection.entry.Metadata)}
}

// Close releases the selection's retained files, without deleting either one.
func (selection *Selection) Close() error {
	if selection == nil {
		return nil
	}
	selection.mu.Lock()
	defer selection.mu.Unlock()
	if selection.closed {
		return selection.closeErr
	}
	selection.closed = true
	if selection.disk != nil {
		selection.closeErr = errors.Join(selection.closeErr, selection.disk.Close())
	}
	if selection.metadata != nil {
		selection.closeErr = errors.Join(selection.closeErr, selection.metadata.Close())
	}
	return selection.closeErr
}

// List returns only complete, checked checkpoint pairs, newest first.
func (s *Store) List(ctx context.Context) ([]Entry, error) {
	if s == nil {
		return nil, failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return nil, err
	}
	return s.list(ctx)
}

func (s *Store) keys(ctx context.Context) (_ []string, returned error) {
	dir, err := s.entries.Open(".")
	if err != nil {
		return nil, failure("cannot open the private checkpoint folder", err)
	}
	defer func() { returned = errors.Join(returned, dir.Close()) }()
	var keys []string
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(256)
		count += len(entries)
		if count > 25000 {
			return nil, failure("the checkpoint folder exceeds its entry bound", nil)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".disk") {
				continue
			}
			key := strings.TrimSuffix(name, ".disk")
			at, err := time.Parse(TimeLayout, key)
			if err != nil || at.Format(TimeLayout) != key {
				continue
			}
			if _, err := s.entries.Lstat(key + ".json"); errors.Is(err, os.ErrNotExist) {
				continue // A crash before pair publication leaves no checkpoint.
			} else if err != nil {
				return nil, failure("cannot inspect checkpoint metadata", err)
			}
			keys = append(keys, key)
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, failure("cannot read the private checkpoint folder", err)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	return keys, nil
}

func (s *Store) list(ctx context.Context) ([]Entry, error) {
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, failure("cannot list checkpoints", err)
	}
	entries := make([]Entry, 0, len(keys))
	for _, key := range keys {
		selected, err := s.openSelection(ctx, key)
		if err != nil {
			return nil, err
		}
		entries = append(entries, selected.Entry())
		if err := selected.Close(); err != nil {
			return nil, err
		}
	}
	return entries, s.checkOwned(ctx, s.check)
}

// Resolve pins checkpoint n, where 1 is newest, before a safety checkpoint is
// added. The caller must Close this original selection after restore or abort.
func (s *Store) Resolve(ctx context.Context, n int) (*Selection, error) {
	if s == nil {
		return nil, failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return nil, err
	}
	keys, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	if n < 1 || n > len(keys) {
		return nil, &proto.Error{Code: "rewind_unknown", Message: "the requested checkpoint does not exist", Hint: "List the session's checkpoints, then choose one of their current numbers."}
	}
	selection, err := s.openSelection(ctx, keys[n-1])
	if err != nil {
		return nil, err
	}
	if err := s.checkOwned(ctx, s.check); err != nil {
		return nil, errors.Join(err, selection.Close())
	}
	return selection, nil
}

func (s *Store) openSelection(ctx context.Context, key string) (_ *Selection, returned error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	disk, err := cachefs.OpenFile(s.entries, key+".disk", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, failure("cannot pin the selected checkpoint disk", err)
	}
	selected := &Selection{store: s, key: key, disk: disk, sequence: s.sequence}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, disk.Close())
			if selected.metadata != nil {
				returned = errors.Join(returned, selected.metadata.Close())
			}
		}
	}()
	selected.metadata, err = cachefs.OpenFile(s.entries, key+".json", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, failure("cannot pin the selected checkpoint metadata", err)
	}
	selected.diskRef, err = cachefs.CaptureFile(s.entries, key+".disk", disk, 0o600)
	if err != nil {
		return nil, err
	}
	selected.metadataRef, err = cachefs.CaptureFile(s.entries, key+".json", selected.metadata, 0o600)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(selected.metadata, maxMetadata+1))
	if err != nil {
		return nil, failure("cannot read the selected checkpoint metadata", err)
	}
	value, err := decodeMetadata(data)
	if err != nil {
		return nil, failure("cannot read selected checkpoint metadata", err)
	}
	at, _ := time.Parse(TimeLayout, key)
	selected.entry = Entry{Time: at, Metadata: value}
	if err := selected.check(s); err != nil {
		return nil, err
	}
	complete = true
	return selected, nil
}

func (selection *Selection) check(s *Store) error {
	if selection == nil || selection.store != s || selection.closed {
		return failure("the checkpoint selection has another owner or is closed", nil)
	}
	return errors.Join(selection.diskRef.CheckFile(s.entries, selection.key+".disk", selection.disk, 0o600), selection.metadataRef.CheckFile(s.entries, selection.key+".json", selection.metadata, 0o600))
}

// Trim removes complete checkpoints older than keep. Rewind calls this only
// after its disk replacement succeeds. keep must be at least one.
func (s *Store) Trim(ctx context.Context, keep int) error {
	if s == nil {
		return failure("the checkpoint store is absent", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, s.check); err != nil {
		return err
	}
	return s.trim(ctx, keep, s.check)
}

// TrimStopped applies retention only after a successful rewind replacement,
// while the SAME native stopped owner still retains the operation.
func (s *Store) TrimStopped(ctx context.Context, keep int, checkStopped func(context.Context) error) error {
	if s == nil || checkStopped == nil {
		return failure("checkpoint retention has no original stopped owner", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOwned(ctx, checkStopped); err != nil {
		return err
	}
	return s.trim(ctx, keep, checkStopped)
}

func (s *Store) trim(ctx context.Context, keep int, owner func(context.Context) error) error {
	if err := s.noPreparedRestoreV1(); err != nil {
		return err
	}
	if keep < 1 || keep > 10000 {
		return failure("the checkpoint retention count is invalid", nil)
	}
	keys, err := s.keys(ctx)
	if err != nil || len(keys) <= keep {
		return err
	}
	for _, key := range keys[keep:] {
		if err := s.checkOwned(ctx, owner); err != nil {
			return err
		}
		selected, err := s.openSelection(ctx, key)
		if err != nil {
			return err
		}
		err = selected.check(s)
		if err == nil {
			err = errors.Join(s.entries.Remove(key+".disk"), s.entries.Remove(key+".json"))
		}
		err = errors.Join(err, selected.Close())
		if err != nil {
			return failure("cannot remove an old checkpoint", err)
		}
	}
	return syncRoot(s.entries)
}
