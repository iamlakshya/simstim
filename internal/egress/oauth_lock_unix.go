//go:build unix

package egress

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
)

// withAuthFileLock serialises host auth-file check/refresh/write-back across
// concurrent sessions on the same host so rotating refresh tokens never race.
func withAuthFileLock(lockPath string, fn func() error) error {
	if lockPath == "" {
		return fn()
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(lockPath)); err != nil {
		return fmt.Errorf("egress oauth: create lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("egress oauth: open lock file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("egress oauth: lock %s: %w", lockPath, err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()
	return fn()
}
