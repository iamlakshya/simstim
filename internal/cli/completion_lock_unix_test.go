//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
)

func holdCompletionHostsFileLock(t *testing.T) {
	t.Helper()
	path := filepath.Join(client.ConfigDir(), "hosts.toml.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	})
}
