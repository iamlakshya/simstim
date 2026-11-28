//go:build unix

package image

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockFile(path string, exclusive bool) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	if err := unix.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
