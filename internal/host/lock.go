package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
)

// lockFile is the file in a session's folder that its lock holds.
const lockFile = "op.lock"

// renameWait is how long a rename waits for op.lock before saying the
// session is busy.
const renameWait = 5 * time.Second

// lockSession takes the lock of session id, so that a resume, a restore,
// a rename and a delete of the same session never run at once. Each would
// stop or start the session's holder, or change what a delete checks. The
// bridge serves each request on a goroutine of its own. Each client also
// has its own bridge. So the lock is a file that all of them share. A
// session that something else holds is busy: lockSession does not wait,
// and the caller says so. The unlock function lets the lock go.
func lockSession(id string) (unlock func(), err error) {
	return lockSessionWait(id, 0)
}

// lockSessionWait is lockSession, and waits up to d for a lock that
// something else holds. A rename waits briefly so it serialises cleanly
// with a delete or a quick start.
func lockSessionWait(id string, d time.Duration) (unlock func(), err error) {
	deadline := time.Now().Add(d)
	for {
		f, err := os.OpenFile(filepath.Join(paths.SessionDir(id), lockFile), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
			}
			return nil, fmt.Errorf("lock session %s: %w", id, err)
		}
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			// A delete may have removed the folder while we waited.
			if _, statErr := os.Stat(paths.SessionDir(id)); errors.Is(statErr, os.ErrNotExist) {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				f.Close()
				return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
			}
			return func() {
				_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
				f.Close()
			}, nil
		}
		f.Close()
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("lock session %s: %w", id, err)
		}
		if d <= 0 || !time.Now().Before(deadline) {
			return nil, UserError("busy", "session "+id+" is busy: a resume, a restore, a rename or a delete of it runs",
				"Wait until it ends, then try again.")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
