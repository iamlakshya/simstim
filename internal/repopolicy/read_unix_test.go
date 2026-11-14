//go:build linux || darwin

package repopolicy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
)

func TestRepositoryPolicySpecialFilesCannotBlockTheReader(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "."+brand.Name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sandbox.toml")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := Read(root, "repo"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO was accepted as policy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("special file blocked the policy reader")
	}
}
