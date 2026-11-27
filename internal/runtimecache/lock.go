package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"os"
	"time"
)

const lockName = ".lock"

// establishLock fixes this Store's lock identity before a caller can start
// transfers. Existing sentinels are opened without truncate, chmod or replace.
// A read-only constructor never creates a sentinel.
func (s *Store) establishLock(create bool) error {
	var f *os.File
	var err error
	created := false
	if create {
		f, err = openNoFollow(s.root, lockName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			f, err = s.openExistingLock()
		} else {
			created = err == nil
		}
	} else {
		f, err = s.openExistingLock()
	}
	if err != nil {
		return fmt.Errorf("establish runtime store lock: %w", err)
	}
	defer f.Close()
	info, err := s.lockSnapshot(f)
	if err != nil {
		return err
	}
	s.lockInfo = info
	if created {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("sync new runtime store lock: %w", err)
		}
		if err := s.syncDir("."); err != nil {
			return err
		}
	}
	return s.checkLockIdentity(f)
}

func (s *Store) openExistingLock() (*os.File, error) {
	flags := os.O_RDWR
	if s.readOnly {
		flags = os.O_RDONLY
	}
	return s.openRegularFlags(lockName, 0o600, flags)
}

// lockSnapshot binds the opened descriptor to the current anchored name.
// Metadata is checked on both views so a link or foreign replacement cannot
// establish a lock domain for this Store.
func (s *Store) lockSnapshot(f *os.File) (os.FileInfo, error) {
	return cachefs.ValidateFile(s.root, lockName, f, 0o600)
}

func (s *Store) checkLockIdentity(f *os.File) error {
	info, err := s.lockSnapshot(f)
	if err != nil {
		return err
	}
	if s.lockInfo == nil || !os.SameFile(s.lockInfo, info) {
		return fmt.Errorf("runtime store lock differs from its constructor identity")
	}
	return nil
}

func (s *Store) locked(ctx context.Context, run func() error) error {
	return s.withLock(ctx, func(file *os.File) error {
		if err := s.checkDomain(ctx, file, false); err != nil {
			return err
		}
		return run()
	})
}

func (s *Store) withLock(ctx context.Context, run func(*os.File) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := s.openExistingLock()
	if err != nil {
		return fmt.Errorf("open existing runtime store lock: %w", err)
	}
	defer f.Close()
	if err := s.checkLockIdentity(f); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		held, err := tryLock(f)
		if err != nil {
			return fmt.Errorf("lock runtime store: %w", err)
		}
		if held {
			if err := s.checkLockIdentity(f); err != nil {
				return err
			}
			break
		}
		if s.onLockBlocked != nil {
			s.onLockBlocked()
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return run(f) // The per-operation descriptor releases its lock on close.
}
