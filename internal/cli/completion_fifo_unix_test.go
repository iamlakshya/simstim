//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The FIFO and permission witnesses need native Unix file semantics. The
// missing, corrupt, oversized and directory cases also run on Windows.
func TestCompletionUnreadableAndFIFOFiles(t *testing.T) {
	cfgDir, stateDir := isolateCompletionEnv(t)

	for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
		_ = os.RemoveAll(p)
		if err := syscall.Mkfifo(p, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		checkEmptyCompletion(t, "fifo")
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("completion blocked on a FIFO")
	}

	if os.Geteuid() != 0 {
		for _, p := range []string{filepath.Join(cfgDir, "hosts.toml"), filepath.Join(cfgDir, "config.toml"), filepath.Join(stateDir, "launcher.json")} {
			_ = os.RemoveAll(p)
			if err := os.WriteFile(p, []byte("x"), 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
		}
		checkEmptyCompletion(t, "unreadable")
	}
}
