//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// The real caller owns this description before waiting. Only its retained
// first native close releases the flock; failures never close a borrowed file.
func waitPurgeOriginalPlacementV4(ctx context.Context, root *os.Root, name string, file *os.File, pinned os.FileInfo) error {
	if ctx == nil || root == nil || file == nil || pinned == nil {
		return placementError("the original placement waiter has no retained native lock")
	}
	if err := errors.Join(ctx.Err(), checkNamedPlacementRoot(root)); err != nil {
		return err
	}
	before, err := cachefs.ValidateFile(root, name, file, 0o600)
	if err != nil || !os.SameFile(before, pinned) {
		return errors.Join(placementError("the original placement lock changed before acquisition"), err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		locked, err := cachefs.TryLock(file, cachefs.Exclusive)
		if err != nil {
			return errors.Join(placementError("cannot acquire the original owned placement lock"), err)
		}
		if locked {
			if err := checkNamedPlacementRoot(root); err != nil {
				return err
			}
			after, err := cachefs.ValidateFile(root, name, file, 0o600)
			if err != nil || !os.SameFile(pinned, after) {
				return errors.Join(placementError("the original placement lock changed during acquisition"), err)
			}
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return UserError("busy", "another operation kept placement busy", "Wait for that operation to finish, then try again.")
		case <-time.After(time.Millisecond):
		}
	}
}
