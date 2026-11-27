package runtimecache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func ageRecord(t *testing.T, s *Store, manifest supply.Manifest, when time.Time) {
	t.Helper()
	if err := s.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(Record{Manifest: manifest, InstalledAt: when})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.root.WriteFile("runtime/"+manifest.Runtime+".json", data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func ageFile(t *testing.T, dir, digest string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(dir, "sha256", digest), when, when); err != nil {
		t.Fatal(err)
	}
}

func TestPruneKeepsNewestTwoEverySessionAndArrivalGrace(t *testing.T) {
	store, dir := testStore(t)
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	manifests := []supply.Manifest{testManifest([]byte("old-runtime"), "r1"), testManifest([]byte("second-runtime"), "r2"), testManifest([]byte("new-runtime"), "r3")}
	for i, m := range manifests {
		installBody(t, store, m.Items[0], []byte([]string{"old-runtime", "second-runtime", "new-runtime"}[i]))
		ageRecord(t, store, m, old.Add(time.Duration(i)*time.Minute))
		ageFile(t, dir, m.Items[0].SHA256, old)
	}
	orphan := testManifest([]byte("orphan"), "unused").Items[0]
	installBody(t, store, orphan, []byte("orphan"))
	ageFile(t, dir, orphan.SHA256, old)
	direct := testManifest([]byte("direct"), "unused").Items[0]
	installBody(t, store, direct, []byte("direct"))
	ageFile(t, dir, direct.SHA256, old)
	recent := testManifest([]byte("recent"), "unused").Items[0]
	installBody(t, store, recent, []byte("recent"))
	ownPart, err := store.Begin(orphan)
	if err != nil {
		t.Fatal(err)
	}
	defer ownPart.Close()
	refs := []References{{Label: "r1"}, {Digests: []string{direct.SHA256}}}
	dry, err := store.Prune(context.Background(), runtime.GOARCH, refs, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry) != 1 || dry[0] != orphan.SHA256 {
		t.Fatalf("wrong prune candidates %v", dry)
	}
	if _, err := os.Stat(filepath.Join(dir, "sha256", orphan.SHA256)); err != nil {
		t.Fatal("dry run removed file")
	}
	removed, err := store.Prune(context.Background(), runtime.GOARCH, refs, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != orphan.SHA256 {
		t.Fatalf("wrong removals %v", removed)
	}
	for _, it := range []supply.Item{manifests[0].Items[0], manifests[1].Items[0], manifests[2].Items[0], direct, recent} {
		if present, err := store.Has(context.Background(), it); err != nil || !present {
			t.Fatalf("retained file lost %s: %t %v", it.Name, present, err)
		}
	}
	if _, err := store.root.Lstat(ownPart.name); err != nil {
		t.Fatalf("prune touched incoming part: %v", err)
	}
}

func TestPruneFailsClosedBeforeAnyDeletion(t *testing.T) {
	for _, kind := range []string{"corrupt record", "missing referenced label", "bad direct digest", "unreadable session refs"} {
		t.Run(kind, func(t *testing.T) {
			store, dir := testStore(t)
			body := []byte("old")
			item := testManifest(body, "r1").Items[0]
			installBody(t, store, item, body)
			ageFile(t, dir, item.SHA256, time.Now().Add(-3*time.Hour))
			var refs []References
			read := func() ([]References, error) { return refs, nil }
			switch kind {
			case "corrupt record":
				if err := store.root.WriteFile("runtime/bad.json", []byte("{"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing referenced label":
				refs = []References{{Label: "missing"}}
			case "bad direct digest":
				refs = []References{{Digests: []string{"../file"}}}
			case "unreadable session refs":
				read = func() ([]References, error) { return nil, os.ErrPermission }
			}
			if removed, err := store.PruneWithReferences(context.Background(), runtime.GOARCH, read, time.Now(), false); err == nil || len(removed) != 0 {
				t.Fatalf("unsafe retention accepted: %v %v", removed, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "sha256", item.SHA256)); err != nil {
				t.Fatal("file removed despite unknown references")
			}
		})
	}
}

func TestRuntimeRecordImmutableLabelAndExistingEntries(t *testing.T) {
	store, _ := testStore(t)
	body := []byte("data")
	m := testManifest(body, "r1")
	if err := store.Commit(context.Background(), m, runtime.GOARCH); err == nil {
		t.Fatal("incomplete runtime recorded")
	}
	installBody(t, store, m.Items[0], body)
	if err := store.Commit(context.Background(), m, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	before, err := store.Records(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(context.Background(), m, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	after, err := store.Records(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || !before[0].InstalledAt.Equal(after[0].InstalledAt) {
		t.Fatal("same label reordered runtime age")
	}
	changed := m
	changed.Items = append([]supply.Item(nil), m.Items...)
	changed.Items[0].Version = "2"
	if err := store.Commit(context.Background(), changed, runtime.GOARCH); err == nil {
		t.Fatal("same label changed entries")
	}
}

func TestPruneLatestTwoWithoutSessionKeepsOnlyThoseLabels(t *testing.T) {
	store, dir := testStore(t)
	now := time.Now()
	old := now.Add(-4 * time.Hour)
	var hashes []string
	for i, body := range []string{"one", "two", "three"} {
		m := testManifest([]byte(body), body)
		installBody(t, store, m.Items[0], []byte(body))
		ageRecord(t, store, m, old.Add(time.Duration(i)*time.Minute))
		ageFile(t, dir, m.Items[0].SHA256, old)
		hashes = append(hashes, m.Items[0].SHA256)
	}
	got, err := store.Prune(context.Background(), runtime.GOARCH, nil, now, false)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != hashes[0] {
		t.Fatalf("latest-two retention %v, oldest %s", got, hashes[0])
	}
}

func TestArrivalGraceBeginsAtPublication(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("delayed")
	item := testManifest(body, "unused").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, part.name), old, old); err != nil {
		t.Fatal(err)
	}
	if err := part.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.Prune(context.Background(), runtime.GOARCH, nil, time.Now(), false); err != nil || len(removed) != 0 {
		t.Fatalf("new arrival lost its grace: %v %v", removed, err)
	}
}
