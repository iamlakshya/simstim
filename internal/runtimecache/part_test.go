package runtimecache

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func cacheTestRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(root, key)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	return root
}
func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(cacheTestRoot(t), "cache")
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, dir
}
func testManifest(body []byte, label string) supply.Manifest {
	item := supply.NewItem(supply.KindUserns, "userns", "1", runtime.GOARCH, body, supply.ReleaseFolder("1.2.3")+"/userns-"+runtime.GOARCH)
	return supply.Manifest{Release: "1.2.3", Runtime: label, Items: []supply.Item{item}}
}
func installBody(t *testing.T, s *Store, it supply.Item, body []byte) {
	t.Helper()
	part, err := s.Begin(it)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Close()
	for offset := 0; offset < len(body); {
		end := min(offset+ChunkSize, len(body))
		if err := part.WriteAt(body[offset:end], int64(offset)); err != nil {
			t.Fatal(err)
		}
		offset = end
	}
	if err := part.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func assertNoParts(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "incoming"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("incoming parts remain: %v", entries)
	}
}

func TestPartChecksWrongDigestAndDeletesOnlyOwnFile(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("expected")
	item := testManifest(body, "r1").Items[0]
	foreign := filepath.Join(dir, "incoming", "foreign.part")
	if err := os.WriteFile(foreign, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt([]byte("modified"), 0); err != nil {
		t.Fatal(err)
	}
	err = part.Finish(context.Background())
	var mismatch *supply.MismatchError
	if !errors.As(err, &mismatch) || mismatch.What != "SHA-256" {
		t.Fatalf("wrong digest: %v", err)
	}
	if _, err := store.root.Lstat(part.name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("own part not deleted: %v", err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "keep" {
		t.Fatalf("foreign part changed: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sha256", item.SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad file published: %v", err)
	}
}

func TestPartRejectsExcessBeforeWritingAndRejectsReplay(t *testing.T) {
	for _, test := range []struct {
		name          string
		first, second []byte
		offset        int64
	}{
		{"oversize", nil, []byte("12345"), 0}, {"replayed offset", []byte("12"), []byte("34"), 0}, {"chunk bound", nil, make([]byte, ChunkSize+1), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, dir := testStore(t)
			body := []byte("1234")
			if test.name == "chunk bound" {
				body = make([]byte, ChunkSize+1)
			}
			item := testManifest(body, "r1").Items[0]
			part, err := store.Begin(item)
			if err != nil {
				t.Fatal(err)
			}
			if len(test.first) > 0 {
				if err := part.WriteAt(test.first, 0); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := part.Progress()
			if err := part.WriteAt(test.second, test.offset); err == nil {
				t.Fatal("invalid chunk accepted")
			}
			after, _ := part.Progress()
			if before != after {
				t.Fatalf("rejected bytes written: %d -> %d", before, after)
			}
			assertNoParts(t, dir)
		})
	}
}

func TestPartConcurrentSameDigestPublishesOneSingleLink(t *testing.T) {
	store, dir := testStore(t)
	body := bytes.Repeat([]byte("x"), ChunkSize+17)
	item := testManifest(body, "r1").Items[0]
	parts := make([]*Part, 2)
	for i := range parts {
		p, err := store.Begin(item)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = p
		for off := 0; off < len(body); {
			end := min(off+ChunkSize, len(body))
			if err := p.WriteAt(body[off:end], int64(off)); err != nil {
				t.Fatal(err)
			}
			off = end
		}
	}
	if parts[0].name == parts[1].name {
		t.Fatal("transfers share a partial file")
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, part := range parts {
		group.Add(1)
		go func(p *Part) { defer group.Done(); <-start; errs <- p.Finish(context.Background()) }(part)
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	f, err := store.OpenItem(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !descriptorSafe(f, st) {
		t.Fatalf("publication is not a safe single-link file: %v", err)
	}
	if !modeMatches(st, 0o444) {
		t.Fatalf("mode %v", st.Mode())
	}
	assertNoParts(t, dir)
}

func TestPartCleanupLeavesForeignReplacement(t *testing.T) {
	store, _ := testStore(t)
	item := testManifest([]byte("data"), "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	saved := part.name + ".saved"
	if err := store.root.Rename(part.name, saved); err != nil {
		t.Fatal(err)
	}
	f, err := store.root.OpenFile(part.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("foreign"))
	f.Close()
	if err := part.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := store.root.ReadFile(part.name)
	if err != nil || string(data) != "foreign" {
		t.Fatalf("foreign replacement removed or changed: %q %v", data, err)
	}
}

func TestPartCancellationKeepsNoOwnPartialFile(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("data")
	item := testManifest(body, "r1").Items[0]
	p, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	assertNoParts(t, dir)
}
