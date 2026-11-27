package checkpoint

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// OpenActorRootV3 is resource storage beneath an actual actor's private VM
// root. It applies the same strict current-UID ownership checks as OpenOwned.
// It grants no native actor, operation or stopped authority. The genuine
// native producer must retain the returned partial store before handling err.
// Its versioned layout is vm/checkpoints; legacy session/checkpoints is never
// opened or reinterpreted here. The original disk and root stay borrowed.
func OpenActorRootV3(ctx context.Context, root *os.Root, disk *os.File, check func(context.Context) error) (*Store, error) {
	if ctx == nil || root == nil || disk == nil || check == nil {
		return nil, failure("the actor checkpoint storage has no actual rooted owner", nil)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return nil, err
	}
	s := &Store{session: root, diskRoot: root, disk: disk, check: check, clone: cloneFile, actorRootV3: true}
	var err error
	s.sessionRef, err = cachefs.CaptureRoot(root)
	if err != nil {
		return s, err
	}
	s.diskRootRef = s.sessionRef
	s.diskRef, err = cachefs.CaptureFile(root, diskName, disk, 0o600)
	if err != nil {
		return s, err
	}
	s.entries, err = cachefs.OpenPrivateChild(root, "checkpoints", true)
	if err != nil {
		return s, err
	}
	s.entriesRef, err = cachefs.CaptureRoot(s.entries)
	if err != nil {
		return s, err
	}
	return s, s.checkOwned(ctx, check)
}

// ListPageV3 bounds one native actor reply to one complete metadata record.
// after is an exclusive timestamp cursor, never a file or source capability.
func (s *Store) ListPageV3(ctx context.Context, after string) (entry *Entry, err error) {
	if s == nil {
		return nil, failure("the actor checkpoint store is absent", nil)
	}
	if after != "" {
		if _, err := timeFromKeyV1(after); err != nil {
			return nil, err
		}
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
	for _, key := range keys {
		if after != "" && key >= after {
			continue
		}
		selected, err := s.openSelection(ctx, key)
		if err != nil {
			return nil, err
		}
		value := selected.Entry()
		if err := errors.Join(selected.Close(), s.checkOwned(ctx, s.check)); err != nil {
			return nil, err
		}
		return &value, nil
	}
	return nil, s.checkOwned(ctx, s.check)
}
