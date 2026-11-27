package host

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

const (
	agentNoFollow = 0
	agentNonblock = 0
)

func privateAgentLock(st os.FileInfo) bool { return st != nil && st.Mode().IsRegular() }

func tryAgentLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY|windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}

func releaseAgentLock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

func privateAgentSocket(_ os.FileInfo) bool { return false }
