package cachefs

import (
	"fmt"
	"os"
)

// LockMode is an explicit shared or exclusive file lease.
type LockMode uint8

const (
	// Shared permits other shared holders.
	Shared LockMode = iota
	// Exclusive excludes every other holder.
	Exclusive
)

// TryLock takes one nonblocking lease on a checked descriptor. The caller
// owns context waiting and must close the descriptor to release its lease.
func TryLock(file *os.File, mode LockMode) (bool, error) {
	if file == nil {
		return false, fmt.Errorf("cache lock descriptor is missing")
	}
	if mode != Shared && mode != Exclusive {
		return false, fmt.Errorf("cache lock mode is invalid")
	}
	return tryLock(file, mode)
}

// SyncDirectory preserves the native directory sync contract. Windows has no
// os.File directory flush here and retains its existing no-op behaviour.
func SyncDirectory(file *os.File) error {
	if file == nil {
		return fmt.Errorf("cache directory descriptor is missing")
	}
	return syncDirectory(file)
}
