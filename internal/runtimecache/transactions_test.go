package runtimecache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPublicationKeepsSingleLinkCheckerBehindTransaction(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("data")
	item := testManifest(body, "r1").Items[0]
	other, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	store.afterLink = func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		defer cancel()
		present, err := other.Has(ctx, item)
		if present || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("reader entered a transient hardlink state: present %t error %v", present, err)
		}
	}
	installBody(t, store, item, body)
	if present, err := other.Has(context.Background(), item); err != nil || !present {
		t.Fatalf("stable publication is not checked: %t %v", present, err)
	}
	assertNoParts(t, dir)
}

func TestPruneRechecksInodeAndArrivalBeforeRemoving(t *testing.T) {
	for _, kind := range []string{"replaced inode", "fresh arrival"} {
		t.Run(kind, func(t *testing.T) {
			store, dir := testStore(t)
			body := []byte("old")
			item := testManifest(body, "unused").Items[0]
			installBody(t, store, item, body)
			old := time.Now().Add(-3 * time.Hour)
			ageFile(t, dir, item.SHA256, old)
			target := filepath.Join(dir, "sha256", item.SHA256)
			store.beforePruneRemove = func(digest string) {
				if digest != item.SHA256 {
					t.Fatal("wrong prune callback")
				}
				if kind == "replaced inode" {
					if err := os.Rename(target, target+".saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(target, []byte("replacement"), 0o444); err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(target, old, old); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chtimes(target, time.Now(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if removed, err := store.Prune(context.Background(), runtime.GOARCH, nil, time.Now(), false); err == nil || len(removed) != 0 {
				t.Fatalf("changed prune candidate removed: %v %v", removed, err)
			}
			if _, err := os.Stat(target); err != nil {
				t.Fatalf("foreign/fresh replacement removed: %v", err)
			}
		})
	}
}

func TestPartRejectsShortFileBeforePublication(t *testing.T) {
	store, dir := testStore(t)
	item := testManifest([]byte("data"), "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt([]byte("dat"), 0); err != nil {
		t.Fatal(err)
	}
	if err := part.Finish(context.Background()); err == nil {
		t.Fatal("short runtime file accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "sha256", item.SHA256)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short runtime file published: %v", err)
	}
	assertNoParts(t, dir)
}
