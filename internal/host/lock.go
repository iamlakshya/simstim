package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
)

const (
	// lockFile is the file in a session's folder that its operation lock holds.
	lockFile = "op.lock"
	// firstLockFile serialises a holder's first manifest save with stopped
	// renames and deletes after a slow restore let go of lockFile.
	firstLockFile = "first.lock"
)

// lockSession takes the lock of session id, so that a resume, a restore,
// a rename and a delete of the same session never run at once. Each would
// stop or start the session's holder, or change what a delete checks. The
// bridge serves each request on a goroutine of its own. Each client also
// has its own bridge. So the lock is a file that all of them share. A
// session that something else holds is busy: lockSession does not wait,
// and the caller says so. The unlock function lets the lock go.
func lockSession(id string) (unlock func(), err error) { return lockSessionWait(id, 0) }

// lockSessionWait is lockSession, and waits up to d for a lock that
// something else holds. A rename waits: it is quick, and so is what it
// waits for, except a resume.
func lockSessionWait(id string, d time.Duration) (unlock func(), err error) {
	return lockFileWait(id, lockFile, d)
}

// lockFirstSave takes the first-save lock of session id, waiting up to d.
func lockFirstSave(id string, d time.Duration) (unlock func(), err error) {
	return lockFileWait(id, firstLockFile, d)
}

// sessionGone reports whether session id's folder has been removed.
func sessionGone(id string) bool {
	_, err := os.Stat(paths.SessionDir(id))
	return errors.Is(err, os.ErrNotExist)
}

func lockFileWait(id, name string, d time.Duration) (unlock func(), err error) {
	return lockFileWaitContext(context.Background(), id, name, d)
}

func lockFileWaitContext(ctx context.Context, id, name string, d time.Duration) (unlock func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(paths.SessionDir(id), name), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}
	deadline := time.Now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				f.Close()
			}, nil
		case !errors.Is(err, unix.EWOULDBLOCK):
			f.Close()
			return nil, fmt.Errorf("lock session %s: %w", id, err)
		case !time.Now().Before(deadline):
			f.Close()
			return nil, UserError("busy", "session "+id+" is busy: a resume, a restore, a rename or a delete of it runs",
				"Wait until it ends, then try again.")
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
