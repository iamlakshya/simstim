package runtimecache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func installPurgeRuntime(t *testing.T, store *Store, body, label string) supply.Manifest {
	t.Helper()
	manifest := testManifest([]byte(body), label)
	installBody(t, store, manifest.Items[0], []byte(body))
	empty := []supply.Item{}
	if err := store.CommitWithImageDefaults(t.Context(), manifest, &empty, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func noRuntimePurgeReferences() ([]References, error) { return nil, nil }

func removeRuntimePurgeEntry(store *Store, entry PurgeEntry) error {
	current, err := store.root.Lstat(entry.Name)
	if err != nil || !os.SameFile(current, entry.Info) {
		return errors.New("test remover refuses changed descriptor identity")
	}
	return store.root.Remove(entry.Name)
}

func TestPurgeRuntimeKeepsEveryRecordUntilAllObjectsFinish(t *testing.T) {
	store, dir := testStore(t)
	one := installPurgeRuntime(t, store, "one", "r1")
	two := installPurgeRuntime(t, store, "two", "r2")
	interrupted := errors.New("simulated durable capture interruption")
	calls := 0
	_, err := store.PurgeOwned(t.Context(), runtime.GOARCH, noRuntimePurgeReferences, func(entry PurgeEntry) error {
		calls++
		if strings.HasPrefix(entry.Name, "runtime/") {
			t.Fatal("record removal started before every blob callback succeeded")
		}
		if calls == 2 {
			return interrupted
		}
		return removeRuntimePurgeEntry(store, entry)
	})
	if !errors.Is(err, interrupted) || calls != 2 {
		t.Fatalf("interruption changed: %d %v", calls, err)
	}
	for _, manifest := range []supply.Manifest{one, two} {
		if _, err := os.Stat(filepath.Join(dir, "runtime", manifest.Runtime+".json")); err != nil {
			t.Fatalf("crash authority was lost: %v", err)
		}
	}
	seenRecord := false
	sentinel, err := store.PurgeOwned(t.Context(), runtime.GOARCH, noRuntimePurgeReferences, func(entry PurgeEntry) error {
		if strings.HasPrefix(entry.Name, "runtime/") {
			seenRecord = true
		} else if seenRecord {
			t.Fatal("a blob followed a record")
		}
		return removeRuntimePurgeEntry(store, entry)
	})
	if err != nil || sentinel == nil || !os.SameFile(sentinel, store.lockInfo) {
		t.Fatalf("retry or terminal sentinel failed: %v", err)
	}
	for _, child := range []string{"sha256", "runtime"} {
		entries, err := os.ReadDir(filepath.Join(dir, child))
		if err != nil || len(entries) != 0 {
			t.Fatalf("owned %s left: %v %v", child, entries, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, lockName)); err != nil {
		t.Fatalf("active store sentinel was unlinked: %v", err)
	}
}

func TestPurgeRuntimeRereadsReferencesAndVerifiesBytesBeforeCallback(t *testing.T) {
	for _, test := range []string{"reference", "reference error", "wrong hash", "wrong mode", "hard link"} {
		t.Run(test, func(t *testing.T) {
			store, dir := testStore(t)
			manifest := installPurgeRuntime(t, store, "body", "r1")
			item := manifest.Items[0]
			path := filepath.Join(dir, "sha256", item.SHA256)
			read := noRuntimePurgeReferences
			switch test {
			case "reference":
				read = func() ([]References, error) { return []References{{Label: "r1"}}, nil }
			case "reference error":
				read = func() ([]References, error) { return nil, errors.New("reference read failed") }
			case "wrong hash":
				if err := os.Chmod(path, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("evil"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o444); err != nil {
					t.Fatal(err)
				}
			case "wrong mode":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "hard link":
				if err := os.Link(path, path+".other"); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			_, err := store.PurgeOwned(t.Context(), runtime.GOARCH, read, func(PurgeEntry) error { calls++; return nil })
			if err == nil || calls != 0 {
				t.Fatalf("unsafe callback admission: %d %v", calls, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("refused object was removed: %v", err)
			}
		})
	}
}

func TestPurgeRuntimePreservesOrphansIncomingAndFutureRecordAuthority(t *testing.T) {
	store, dir := testStore(t)
	installPurgeRuntime(t, store, "body", "r1")
	orphan := strings.Repeat("f", 64)
	for _, path := range []string{"sha256/" + orphan, "incoming/foreign.part", "runtime/.record-foreign"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, err := store.PurgeOwned(t.Context(), runtime.GOARCH, noRuntimePurgeReferences, func(entry PurgeEntry) error {
		if strings.Contains(entry.Name, orphan) || strings.Contains(entry.Name, "foreign") {
			t.Fatalf("unknown file adopted: %s", entry.Name)
		}
		return removeRuntimePurgeEntry(store, entry)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"sha256/" + orphan, "incoming/foreign.part", "runtime/.record-foreign"} {
		if body, err := os.ReadFile(filepath.Join(dir, path)); err != nil || string(body) != "foreign" {
			t.Fatalf("unknown bytes changed: %s %q %v", path, body, err)
		}
	}
	installPurgeRuntime(t, store, "second", "r2")
	name := filepath.Join(dir, "runtime", "r2.json")
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_authority"] = json.RawMessage(`{"retain":true}`)
	body, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, body, 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if _, err := store.PurgeOwned(t.Context(), runtime.GOARCH, noRuntimePurgeReferences, func(PurgeEntry) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("future authority was adopted: %d %v", calls, err)
	}
}

func TestOpenForPurgeNeverRecreatesMissingChildrenOrSentinel(t *testing.T) {
	store, dir := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sha256", "runtime", "incoming"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	opened, err := OpenForPurge(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened.PurgeOwned(t.Context(), runtime.GOARCH, noRuntimePurgeReferences, func(PurgeEntry) error { t.Fatal("missing child nominated"); return nil }); err != nil {
		t.Fatal(err)
	}
	opened.Close()
	for _, name := range []string{"sha256", "runtime", "incoming"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("child was recreated: %s %v", name, err)
		}
	}
	if err := os.Remove(filepath.Join(dir, lockName)); err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenForPurge(dir); err == nil {
		opened.Close()
		t.Fatal("missing sentinel created a new domain")
	}
	if _, err := os.Lstat(filepath.Join(dir, lockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sentinel recreated: %v", err)
	}
}

func TestPurgeRuntimeCancellationStopsBeforeNextCallback(t *testing.T) {
	store, _ := testStore(t)
	installPurgeRuntime(t, store, "one", "r1")
	installPurgeRuntime(t, store, "two", "r2")
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := store.PurgeOwned(ctx, runtime.GOARCH, noRuntimePurgeReferences, func(entry PurgeEntry) error {
		calls++
		cancel()
		return removeRuntimePurgeEntry(store, entry)
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancel continued removal: %d %v", calls, err)
	}
}
