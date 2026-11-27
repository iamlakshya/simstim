package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentClientPinsNeverOverwriteEachOther(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	failures := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wait.Add(1)
		go func(name string) {
			defer wait.Done()
			failures <- UpdateLock(context.Background(), dir, func(lock *Lock) error {
				lock.Images = append(lock.Images, Pin{Ref: "registry.example/team/" + name, Digest: "sha256:" + strings.Repeat("a", 64), PinnedAt: time.Now().UTC()})
				return nil
			})
		}(name)
	}
	wait.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := UpdateLock(context.Background(), dir, func(lock *Lock) error {
		if len(lock.Images) != 2 {
			t.Fatalf("lost concurrent image pin: %+v", lock.Images)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedClientLockNeverResetsPins(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "images.lock")
	before := []byte("version = 1\n[[image]]\nref = [\n")
	if err := os.WriteFile(name, before, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := UpdateLock(context.Background(), dir, func(*Lock) error { called = true; return nil }); err == nil || called {
		t.Fatal("malformed lock reached update")
	}
	after, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("malformed lock was replaced")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := UpdateLock(cancelled, dir, func(*Lock) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock: %v", err)
	}
}
