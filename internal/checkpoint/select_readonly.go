package checkpoint

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// SelectReadonlyV1 pins immutable checkpoint data below the actual caller's
// retained protected root. It opens no current disk and grants no runtime,
// stopped-session or mutation authority. The returned read caps outlive this
// call; a later actual stopped backend must independently prove the same root.
func SelectReadonlyV1(ctx context.Context, session *os.Root, name string, check func(context.Context) error) (SnapshotV1, error) {
	if ctx == nil || session == nil || check == nil {
		return nil, failure("the read-only checkpoint selection has no original owner", nil)
	}
	at, err := time.Parse(TimeLayout, name)
	if err != nil || at.Format(TimeLayout) != name {
		return nil, failure("the checkpoint name is not a fixed UTC timestamp", err)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return nil, err
	}
	entries, err := cachefs.OpenPrivateChild(session, "checkpoints", false)
	if err != nil {
		return nil, failure("cannot retain the selected checkpoint folder", err)
	}
	selected, err := retainLocalSnapshotV1(session, entries, name, nil)
	closeErr := entries.Close()
	if err != nil || closeErr != nil {
		if selected != nil {
			closeErr = errors.Join(closeErr, selected.Close())
		}
		return nil, errors.Join(err, closeErr)
	}
	if err := errors.Join(ctx.Err(), check(ctx)); err != nil {
		return nil, errors.Join(err, selected.Close())
	}
	return selected, nil
}
