//go:build unix

package diag

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// markInUse takes a shared lock on f, an open debug file, until it is
// closed, so that the turnover of its folder leaves it alone, in this
// process and in any other. It waits for the lock: a turnover holds its
// own lock on a file only for the moment it takes to test it.
func markInUse(f *os.File) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_SH)
		if !errors.Is(err, unix.EINTR) {
			return
		}
	}
}

// inUse reports whether a File, or a writer, has path open: whether its
// lock is taken.
func inUse(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil
}
