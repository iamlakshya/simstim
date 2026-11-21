//go:build linux

package runtimecache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// OpenHostExecution is the explicit original boot owner's existing-store
// view. Unlike an inventory/read-only view, it may create OWN mode-lease
// metadata on a genuinely writable filesystem. It adopts no new domain and
// creates no root, binding or data object. Need/OP/slot authority remains in
// the actual engine caller, never in the public domain tuple.
func OpenHostExecution(ctx context.Context, dir string, domain cachefs.Domain) (*Store, error) {
	store, err := OpenHostReadOnly(ctx, dir, domain)
	if err != nil {
		return nil, err
	}
	store.execution = true
	return store, nil
}

// PrepareRuntime uses the original anchored native store and manifest item.
// The mode phase is still LAZY: no artifact lease survives this transaction.
// BeginExecMode later runs after Reference writes and before projections.
func (s *Store) PrepareRuntime(ctx context.Context, item supply.Item) (*supply.VerifiedRuntime, error) {
	if !s.execution && !s.domain.IsZero() {
		return nil, fmt.Errorf("runtime execution requires its original execution store view")
	}
	var owner *supply.VerifiedRuntime
	err := retryExecMode(ctx, func() error {
		return s.locked(ctx, func() error {
			_, done, err := s.artifactModeGuard(ctx, item)
			if err != nil {
				return err
			}
			defer done()
			file, err := s.openItemUnfencedContext(ctx, item)
			if err != nil {
				return err
			}
			defer file.Close()
			if item.Kind != supply.KindVMM {
				owner, err = supply.PrepareRuntime(ctx, item, file)
				return err
			}
			var fs unix.Statfs_t
			if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
				return err
			}
			if fs.Flags&unix.ST_RDONLY != 0 {
				owner, err = supply.PrepareRuntime(ctx, item, file)
				return err
			}
			artifacts, err := cachefs.OpenPrivateChild(s.root, "sha256", false)
			if err != nil {
				return err
			}
			defer artifacts.Close()
			modes, err := cachefs.OpenPrivateChild(s.root, "execmode", true)
			if err != nil {
				return err
			}
			defer modes.Close()
			owner, err = supply.PrepareRuntimeWithModeV1(ctx, item, file, artifacts, modes, s.root, s.domain)
			return err
		})
	})
	// The actual caller adopts this partial owner before its error branch.
	return owner, err
}

func (s *Store) artifactModeGuard(ctx context.Context, item supply.Item) (*supply.RuntimeModeGuard, func(), error) {
	modes, err := cachefs.OpenPrivateChild(s.root, "execmode", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, func() {}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	artifacts, err := cachefs.OpenPrivateChild(s.root, "sha256", false)
	if err != nil {
		modes.Close()
		return nil, nil, err
	}
	guard, err := supply.TryRuntimeModeGuard(ctx, artifacts, modes, item)
	closeRoots := func() { artifacts.Close(); modes.Close() }
	if err != nil {
		closeRoots()
		return nil, nil, err
	}
	if guard != nil {
		if err := guard.Recover(ctx); err != nil {
			guard.Close()
			closeRoots()
			return nil, nil, err
		}
	}
	return guard, func() {
		if guard != nil {
			guard.Close()
		}
		closeRoots()
	}, nil
}

func retryExecMode(ctx context.Context, run func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := run()
		if !errors.Is(err, supply.ErrRuntimeModeBusy) {
			return err
		}
		// Caller has returned from BOTH store transaction and placement gate.
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) checkPruneModeIntent(digest string, known bool) error {
	if known {
		return nil
	}
	modes, err := cachefs.OpenPrivateChild(s.root, "execmode", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer modes.Close()
	_, err = modes.Lstat(digest + ".lease")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("runtime execution intent has no original record; preserve it for owner review")
}
