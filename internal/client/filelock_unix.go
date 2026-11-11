//go:build !windows

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes a flock on f without waiting, exclusive or shared. It
// reports false when another open file holds it in a way that excludes it.
func tryLockFile(f *os.File, exclusive bool) (bool, error) {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return false, err //nolint:wrapcheck // lockPath wraps it with the path
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:wrapcheck // the caller ignores it
}
