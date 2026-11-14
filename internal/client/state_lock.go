package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// stateFileWaitKey carries a scoped observer for a real contended lock.
type stateFileWaitKey struct{}

type stateFileLock struct {
	file   *os.File
	name   string
	before os.FileInfo
}

// lockStateFile remains the package compatibility entry to the one lock path.
func lockStateFile(ctx context.Context, r *os.Root, name string) (func(), error) {
	held, err := acquireStateFileLock(ctx, r, name, true)
	if err != nil {
		return nil, err
	}
	return held.close, nil
}

func acquireStateFileLock(ctx context.Context, r *os.Root, name string, create bool) (*stateFileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lockName := name + ".lock"
	if st, err := r.Lstat(lockName); err == nil && !st.Mode().IsRegular() {
		return nil, badGrantStateError(lockName)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	flags := os.O_RDWR | grantsStateNoFollow | grantsStateNonblock
	if create {
		flags |= os.O_CREATE
	}
	f, err := r.OpenFile(lockName, flags, 0600)
	if err != nil {
		return nil, err
	}
	before, err := f.Stat()
	if stopped := ctx.Err(); stopped != nil {
		_ = f.Close()
		return nil, stopped
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := stateRegularMatchesContext(ctx, r, lockName, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	held := &stateFileLock{file: f, name: lockName, before: before}
	timer := time.NewTimer(hostsWait)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		ok, err := tryLockFile(f, true)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if ok {
			if err := held.check(ctx, r); err != nil {
				held.close()
				return nil, err
			}
			return held, nil
		}
		if observe, _ := ctx.Value(stateFileWaitKey{}).(func()); observe != nil {
			observe()
		}
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
			_ = f.Close()
			return nil, &UserError{Msg: fmt.Sprintf("another command kept %s locked for %s", name, hostsWait), Hint: "End that command, then run this one again.", Code: "grant_busy", Kind: KindFailed}
		case <-tick.C:
		}
	}
}

func (held *stateFileLock) check(ctx context.Context, r *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := held.file.Stat()
	if stopped := ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil || !stateSnapshotSame(held.before, now) {
		return badGrantStateError(held.name)
	}
	if err := stateRegularMatchesContext(ctx, r, held.name, held.file); err != nil {
		return err
	}
	return ctx.Err()
}

func (held *stateFileLock) close() { _ = held.closeOwned(context.Background()) }

func (held *stateFileLock) closeOwned(ctx context.Context) error {
	unlockErr := unlockFile(held.file)
	closeErr := closeStateOwnedFile(ctx, held.file)
	return errors.Join(unlockErr, closeErr)
}
