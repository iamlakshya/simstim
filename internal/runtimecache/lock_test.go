package runtimecache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStoreConstructorPinsOwnedLockBeforeTransfers(t *testing.T) {
	store, dir := testStore(t)
	f, err := store.openExistingLock()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.Lstat(filepath.Join(dir, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	if store.lockInfo == nil || !os.SameFile(store.lockInfo, info) || !os.SameFile(info, current) || !modeMatches(info, 0o600) || !descriptorSafe(f, info) {
		t.Fatal("constructor did not establish the owned regular 0600 sentinel with one link")
	}
	assertNoParts(t, dir)
}

func TestStoreReadOnlyNeverCreatesMissingSentinel(t *testing.T) {
	store, dir := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, ".lock")); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenReadOnly(dir); err == nil {
		opened.Close()
		t.Fatal("read-only open recreated a missing sentinel")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing sentinel error: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open changed the missing sentinel: %v", err)
	}
}

func TestStoreTransactionNeverRecreatesMissingSentinel(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("data")
	item := testManifest(body, "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Remove(".lock"); err != nil {
		t.Fatal(err)
	}
	if err := part.Finish(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing active sentinel accepted or error lost: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction recreated the missing sentinel: %v", err)
	}
	if _, err := store.root.Lstat("sha256/" + item.SHA256); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction published without its pinned lock: %v", err)
	}
	assertNoParts(t, dir)
}

func replaceStoreLock(t *testing.T, store *Store) {
	t.Helper()
	if err := store.root.Rename(".lock", ".lock.original"); err != nil {
		t.Fatal(err)
	}
	if err := store.root.WriteFile(".lock", []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreTransactionRefusesReplacedSentinel(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("data")
	item := testManifest(body, "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	replaceStoreLock(t, store)
	if err := part.Finish(context.Background()); err == nil {
		t.Fatal("replacement active sentinel started a new lock domain")
	}
	if data, err := store.root.ReadFile(".lock"); err != nil || string(data) != "replacement" {
		t.Fatalf("replacement sentinel changed: %q %v", data, err)
	}
	if _, err := store.root.Lstat("sha256/" + item.SHA256); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement sentinel allowed publication: %v", err)
	}
	assertNoParts(t, dir)
}

func TestStoreTransactionRechecksIdentityAfterAcquiringLock(t *testing.T) {
	store, _ := testStore(t)
	held, err := store.openExistingLock()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if acquired, err := tryLock(held); err != nil || !acquired {
		t.Fatalf("could not hold fixture lock: %t %v", acquired, err)
	}
	waiting := make(chan struct{})
	var once sync.Once
	store.onLockBlocked = func() { once.Do(func() { close(waiting) }) }
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	var entered atomic.Bool
	go func() { result <- store.locked(ctx, func() error { entered.Store(true); return nil }) }()
	select {
	case <-waiting:
	case <-ctx.Done():
		t.Fatal("transaction did not wait on the actual held lock")
	}
	replaceStoreLock(t, store)
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil || entered.Load() {
			t.Fatalf("transaction entered after its sentinel changed while waiting: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("transaction did not finish after releasing the actual lock")
	}
	if data, err := store.root.ReadFile(".lock"); err != nil || string(data) != "replacement" {
		t.Fatalf("waiting transaction changed the replacement: %q %v", data, err)
	}
}

func TestConcurrentWritableConstructorsConvergeOnOneSentinel(t *testing.T) {
	dir := filepath.Join(cacheTestRoot(t), "shared-cache")
	type opened struct {
		store *Store
		err   error
	}
	const constructors = 8
	start := make(chan struct{})
	results := make(chan opened, constructors)
	var workers sync.WaitGroup
	for range constructors {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; s, err := Open(dir); results <- opened{s, err} }()
	}
	close(start)
	workers.Wait()
	close(results)
	var identity os.FileInfo
	count := 0
	for result := range results {
		if result.err != nil {
			t.Errorf("concurrent constructor failed: %v", result.err)
			continue
		}
		defer result.store.Close()
		count++
		if identity == nil {
			identity = result.store.lockInfo
		}
		if result.store.lockInfo == nil || !os.SameFile(identity, result.store.lockInfo) {
			t.Error("constructors chose different lock domains")
		}
	}
	if count != constructors {
		t.Fatalf("only %d constructors converged", count)
	}
	info, err := os.Lstat(filepath.Join(dir, ".lock"))
	if err != nil || !os.SameFile(identity, info) {
		t.Fatalf("anchored sentinel differs from constructor identities: %v", err)
	}
}
