//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/paths"
)

func discoveryFixture(t *testing.T) *EventsDiscovery {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	for _, path := range []string{paths.StateDir(), paths.RuntimeDir()} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d, err := OpenEventsDiscovery()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	return d
}

func TestEventsDiscoveryReadsNativeDirectoryChangeWithoutPolling(t *testing.T) {
	d := discoveryFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	// A queued native create is sufficient. No session or listener is started.
	if err := os.Mkdir(filepath.Join(paths.RuntimeDir(), "s"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.Next(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.RuntimeDir(), "s", "abcd2345.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Next(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestEventsDiscoveryCancellationAndCloseJoinOwnedReader(t *testing.T) {
	d := discoveryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- d.Next(ctx) }()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("native read cancel=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled native discovery reader did not join")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("the original discovery descriptor stayed open: %v", err)
	}
}

func TestEventsDiscoveryRejectsReplacementOfOriginalRoot(t *testing.T) {
	d := discoveryFixture(t)
	path := paths.RuntimeDir()
	if err := os.Rename(path, path+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := d.Next(ctx); err == nil {
		t.Fatal("a replacement directory renewed the original native watch")
	}
}
