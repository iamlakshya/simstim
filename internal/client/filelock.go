package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// errLockBusy says that another holder kept a lock for the whole wait.
var errLockBusy = errors.New("another client process holds the lock")

// lockPath takes a lock on the file at path, which it makes with mode 0600
// when it is missing: an exclusive one, or a shared one that other shared
// holders may hold too. It waits up to wait for the holders that it must
// not share with. The unlock function lets it go.
func lockPath(path string, wait time.Duration, exclusive bool) (unlock func(), err error) {
	return lockPathContext(context.Background(), path, wait, exclusive)
}

// lockPathContext keeps the same bounded lock wait and lets cancellation win.
func lockPathContext(ctx context.Context, path string, wait time.Duration, exclusive bool) (unlock func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the lock %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		ok, err := tryLockFile(f, exclusive)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if ok {
			if err := ctx.Err(); err != nil {
				_ = unlockFile(f)
				_ = f.Close()
				return nil, err
			}
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, errLockBusy)
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
