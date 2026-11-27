//go:build unix

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive lock on f, which ends when f closes. It
// fails with errLockTaken when another open file holds it, in this
// process or in another.
func lockExclusive(f *os.File) error {
	return flock(f, unix.LOCK_EX|unix.LOCK_NB)
}

func flock(f *os.File, how int) error {
	for {
		err := unix.Flock(int(f.Fd()), how)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EWOULDBLOCK):
			return errLockTaken
		case !errors.Is(err, unix.EINTR):
			return err //nolint:wrapcheck // the callers wrap it with what they lock
		}
	}
}
