package client

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// errLockBusy says that another holder kept a lock for the whole wait.
var errLockBusy = errors.New("another client process holds the lock")

// lockPath takes a lock on the file at path, which it makes with mode 0600
// when it is missing: an exclusive one, or a shared one that other shared
// holders may hold too. It waits up to wait for the holders that it must
// not share with. The unlock function lets it go.
func lockPath(path string, wait time.Duration, exclusive bool) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open the lock %s: %w", path, err)
	}
	deadline := time.Now().Add(wait)
	for {
		ok, err := tryLockFile(f, exclusive)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if ok {
			return func() {
				_ = unlockFile(f)
				_ = f.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, errLockBusy)
		}
		time.Sleep(time.Millisecond)
	}
}
