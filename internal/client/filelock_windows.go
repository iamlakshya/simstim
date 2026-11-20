//go:build windows

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a LockFileEx lock on the first byte of f without
// waiting, exclusive or shared. It reports false when another handle holds
// it in a way that excludes it.
func tryLockFile(f *os.File, exclusive bool) (bool, error) {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return false, err //nolint:wrapcheck // lockPath wraps it with the path
}

func unlockFile(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped)) //nolint:wrapcheck // the caller ignores it
}
