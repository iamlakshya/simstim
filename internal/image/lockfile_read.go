package image

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// ReadLock reads one complete client pin snapshot without writing or replacing
// it. UpdateLock publishes by atomic rename; mutations still use its lock.
func ReadLock(ctx context.Context, dir string) (Lock, error) {
	if err := ctx.Err(); err != nil {
		return Lock{}, err
	}
	root, err := cachefs.OpenPrivateRoot(dir, false)
	if errors.Is(err, os.ErrNotExist) {
		return Lock{Version: 1}, nil
	}
	if err != nil {
		return Lock{}, err
	}
	defer root.Close()
	lock, err := readClientLock(&Store{root: root})
	if err != nil {
		return Lock{}, err
	}
	return lock, ctx.Err()
}
