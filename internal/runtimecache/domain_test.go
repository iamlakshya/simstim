package runtimecache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func runtimeDomainFixture() cachefs.Domain {
	return cachefs.Domain{Scope: strings.Repeat("a", 64), HostID: strings.Repeat("b", 32)}
}

func runtimeDomainGate(ctx context.Context, change func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return change()
}

func TestRuntimeHostDomainBindsActualSentinelBeforeCreatingChildren(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	domain := runtimeDomainFixture()
	gateCalls := 0
	store, err := OpenHost(t.Context(), dir, domain, func(ctx context.Context, change func() error) error {
		gateCalls++
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("host constructor created filesystem state before final admission", err)
		}
		return runtimeDomainGate(ctx, change)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if gateCalls != 1 {
		t.Fatal("constructor was not admitted exactly once", gateCalls)
	}
	lock, err := store.openExistingLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	actual, found, err := cachefs.ReadDomainBinding(t.Context(), store.root, lockName, lock)
	if err != nil || !found || actual != domain {
		t.Fatal("actual sentinel has no exact domain binding", actual, found, err)
	}
	for _, name := range []string{"sha256", "incoming", "runtime"} {
		if info, err := store.root.Lstat(name); err != nil || !info.IsDir() {
			t.Fatal("bound store child missing", name, err)
		}
	}
}

func TestRuntimeHostDomainRefusesAnotherHomeAndFixtureFallback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	domain := runtimeDomainFixture()
	store, err := OpenHost(t.Context(), dir, domain, runtimeDomainGate)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	otherHome := domain
	otherHome.Scope = strings.Repeat("c", 64)
	for _, open := range []func() (*Store, error){
		func() (*Store, error) { return OpenHost(t.Context(), dir, otherHome, runtimeDomainGate) },
		func() (*Store, error) { return OpenHostReadOnly(t.Context(), dir, otherHome) },
		func() (*Store, error) { return OpenHostForPurge(t.Context(), dir, otherHome) },
		func() (*Store, error) { return Open(dir) },
		func() (*Store, error) { return OpenReadOnly(dir) },
		func() (*Store, error) { return OpenForPurge(dir) },
	} {
		if opened, err := open(); err == nil {
			opened.Close()
			t.Fatal("different home or context-free fallback adopted host-bound storage")
		}
	}
	after, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused foreign admission changed the owner binding", err)
	}
	if readOnly, err := OpenHostReadOnly(t.Context(), dir, domain); err != nil {
		t.Fatal("same domain lost valid read-only admission", err)
	} else {
		readOnly.Close()
	}
}

func TestRuntimeHostDomainPreservesPopulatedUnboundStorage(t *testing.T) {
	fixture, dir := testStore(t)
	body := []byte("legacy own-account bytes")
	path := filepath.Join(dir, "sha256", strings.Repeat("a", 64))
	if err := os.WriteFile(path, body, 0o444); err != nil {
		t.Fatal(err)
	}
	fixture.Close()
	if opened, err := OpenHost(t.Context(), dir, runtimeDomainFixture(), runtimeDomainGate); err == nil {
		opened.Close()
		t.Fatal("an item digest or familiar legacy directory was adopted as host authority")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatal("legacy blob was changed on refused admission", err)
	}
	lock, err := os.Stat(filepath.Join(dir, lockName))
	if err != nil || lock.Size() != 0 {
		t.Fatal("refused legacy adoption wrote a binding", err)
	}
}

func TestRuntimeHostReadOnlyAndPurgeNeverCreateOrInitialise(t *testing.T) {
	domain := runtimeDomainFixture()
	for _, existing := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "store")
		if existing {
			legacy, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			legacy.Close()
		}
		for _, open := range []func() (*Store, error){
			func() (*Store, error) { return OpenHostReadOnly(t.Context(), dir, domain) },
			func() (*Store, error) { return OpenHostForPurge(t.Context(), dir, domain) },
		} {
			if opened, err := open(); err == nil {
				opened.Close()
				t.Fatal("read-only admission initialised unbound storage")
			}
		}
		if !existing {
			if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only admission created a namespace", err)
			}
		} else if info, err := os.Stat(filepath.Join(dir, lockName)); err != nil || info.Size() != 0 {
			t.Fatal("read-only admission changed the legacy sentinel", err)
		}
	}
}

func TestRuntimeHostPartCreationUsesExplicitContextAndFinalGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	var calls atomic.Int32
	allow := true
	store, err := OpenHost(t.Context(), dir, runtimeDomainFixture(), func(ctx context.Context, change func() error) error {
		calls.Add(1)
		if !allow {
			return context.Canceled
		}
		return runtimeDomainGate(ctx, change)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := testManifest([]byte("body"), "r1").Items[0]
	if _, err := store.Begin(item); err == nil {
		t.Fatal("host transfer borrowed a context-free fixture entry")
	}
	allow = false
	if _, err := store.BeginContext(t.Context(), item); !errors.Is(err, context.Canceled) {
		t.Fatal("part creation ignored final drain admission", err)
	}
	assertNoParts(t, dir)
	allow = true
	part, err := store.BeginContext(t.Context(), item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt([]byte("body"), 0); err != nil {
		t.Fatal(err)
	}
	if err := part.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	part.Close()
	if calls.Load() != 4 {
		t.Fatal("constructor, refused part, admitted part and final publication lost separate gates", calls.Load())
	}
}

func TestRuntimeHostConstructorsConvergeOnOneBoundSentinel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	domain := runtimeDomainFixture()
	const constructors = 6
	var wait sync.WaitGroup
	results := make(chan *Store, constructors)
	failures := make(chan error, constructors)
	for range constructors {
		wait.Go(func() {
			store, err := OpenHost(t.Context(), dir, domain, runtimeDomainGate)
			results <- store
			failures <- err
		})
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal("simultaneous same-domain construction failed", err)
		}
	}
	info, err := os.Lstat(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	for store := range results {
		if store == nil || !os.SameFile(info, store.lockInfo) {
			t.Fatal("constructors split the physical lock domain")
		}
		if _, err := store.RecordsContext(t.Context(), runtime.GOARCH); err != nil {
			t.Fatal("bound constructor left a partial child namespace", err)
		}
		store.Close()
	}
}

func TestOpenHostCreatedRequiresObserver(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store")
	if store, err := OpenHostCreated(t.Context(), dir, runtimeDomainFixture(), runtimeDomainGate, nil); err == nil {
		store.Close()
		t.Fatal("OpenHostCreated accepted nil observer")
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenHostCreated with nil observer created directory: %v", err)
	}
}
