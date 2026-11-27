package host

import (
	"context"
	"errors"
	"os"
	"time"

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

// lockFileWaitContext retains the same native lease while a pending owner waits.
// Cancellation ends admission without leaving a deferred waiter behind.
func lockFileWaitContext(ctx context.Context, id, name string, d time.Duration) (func(), error) {
	lease, err := openOwnedSessionFileLease(ctx, id, name, d)
	if err != nil {
		return nil, err
	}
	return func() { _ = lease.Close() }, nil
}
