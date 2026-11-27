//go:build linux

package runtimecache

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestPartActualLateWriterCloseNeverLinksArtifactV4(t *testing.T) {
	root := os.Getenv("RUNTIME_PART_LATE_CLOSE_ROOT")
	if root == "" {
		t.Skip("Root must admit a real private filesystem whose writer Close returns a genuine late errno")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		t.Fatal("the admitted native close root is invalid")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := supply.CheckFileCloseThreadV1(); err != nil {
		t.Skip("an admitted native close thread is required: " + err.Error())
	}
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	body := []byte("genuine writer close failure")
	item := testManifest(body, "late-close").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	first := part.Finish(t.Context())
	if first == nil || part.ioFailure == nil || !part.writerAttempted || !part.writerReleased {
		t.Fatal("the real writer close did not retain its IO failure and retired descriptor", first)
	}
	if part.linked {
		t.Fatal("retired-but-failed writer became a publication")
	}
	if _, err := store.root.Lstat("sha256/" + item.SHA256); !os.IsNotExist(err) {
		t.Fatal("a failed writer linked publishable bytes", err)
	}
	if err := part.Finish(t.Context()); err == nil {
		t.Fatal("a cleanup retry turned the failed operation into publish success")
	}
	if part.ioFailure == nil {
		t.Fatal("cleanup erased the original IO failure")
	}
	// Close may finish cleanup only after each actual directory sync/close.
	// It never retries the old numeric writer slot and never publishes it.
	_ = part.Close()
	if part.linked {
		t.Fatal("cleanup linked a failed writer")
	}
}
