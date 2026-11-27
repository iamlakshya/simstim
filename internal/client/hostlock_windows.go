//go:build windows

package client

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive lock on f, which ends when f closes. It
// fails with errLockTaken when another open file holds it.
func lockExclusive(f *os.File) error {
	return lockFileEx(f, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func lockFileEx(f *os.File, flags uint32) error {
	var o windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, &o)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockTaken
	}
	return err //nolint:wrapcheck // the callers wrap it with what they lock
}
