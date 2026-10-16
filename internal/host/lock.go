package host

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
)

// lockFile is the file in a session's folder that its lock holds.
const lockFile = "op.lock"

// lockSession takes the lock of session id, so that a resume, a restore
// and a delete of the same session never run at once. Each would stop or
// start the session's holder. The bridge serves each request on a
// goroutine of its own. Each client also has its own bridge. So the lock is
// a file that all of them share. A session that something else holds is
// busy: lockSession does not wait, and the caller says so. The unlock
// function lets the lock go.
func lockSession(id string) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(paths.SessionDir(id), lockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, UserError("not_found", "no session "+id, "List sessions to find its ID.")
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, UserError("busy", "session "+id+" is busy: a resume, a restore or a delete of it runs",
				"Wait until it ends, then try again.")
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
	}, nil
}
