package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func finalGuardLeaf(scope string) string { return "." + brand.Name + "-purge-final-" + scope }

// withPlacementHome serialises entry and terminal role changes through an
// existing checked HOME directory descriptor. It creates no lock sentinel.
// The order is HOME before control. No client I/O runs in its callback.
func withPlacementHome(ctx context.Context, body func(*os.Root) error) error {
	path, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(path) {
		return placementError("cannot identify the persistent home admission root")
	}
	root, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		return placementError("the home admission root is linked, foreign or writable by others")
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		held, err := cachefs.TryLock(dir, cachefs.Exclusive)
		if err != nil {
			return UserError("placement_lock", "this home filesystem cannot serialise directory admission", "Use a local home filesystem that supports native directory locks, then retry the same command.")
		}
		if held {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return UserError("busy", "another entry or final cleanup kept home admission busy", "Wait for that operation to finish, then retry the same command.")
		case <-time.After(time.Millisecond):
		}
	}
	if err := checkNamedPlacementRoot(root); err != nil {
		return err
	}
	return body(root)
}

func refuseFinalisingEntry(home *os.Root, scope, canonical string) error {
	if _, err := home.Lstat(finalGuardLeaf(scope)); err == nil {
		return UserError("draining", "this root scope has a terminal removal still to finish", "Retry the same host rm --purge command before installing or starting another session.")
	} else if !errors.Is(err, os.ErrNotExist) {
		return placementError("the fixed final guard role cannot be inspected safely")
	}
	if info, err := home.Lstat(canonical); err == nil && !info.IsDir() {
		return placementError("the canonical guard role contains a retained terminal marker or foreign object")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
