//go:build linux

package runtimecache

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

// Explicit validator admission is required: this exercises the REAL transfer
// close/publication/enable path on an existing fs-verity-capable filesystem.
func TestNativeVerityTransferClosesWriterBeforeEnable(t *testing.T) {
	parent := os.Getenv("FSVERITY_NATIVE_FIXTURE")
	if parent == "" {
		t.Skip("no admitted native fs-verity filesystem; no transfer ioctl proof claimed")
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("fixture must be an absolute admitted directory")
	}
	dir, err := os.MkdirTemp(parent, "verity-transfer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	store, err := Open(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	body := []byte("actual fixed kernel transfer fixture\n")
	item := supply.NewItem(supply.KindKernel, "guest", "6.18", runtime.GOARCH, body, supply.ReleaseFolder("1.2.3")+"/"+supply.KernelAsset(runtime.GOARCH))
	item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", supply.ReleaseFolder("1.2.3")+"/"+supply.KernelSourceAsset(item.Version), "2026-09-28"
	installBody(t, store, item, body)
	manifest := supply.Manifest{Release: "1.2.3", Runtime: "native-verity", Items: []supply.Item{item}}
	if err := store.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	file, err := store.OpenItem(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	owner, err := supply.PrepareRuntime(context.Background(), item, file)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	borrowed, err := owner.File()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := file.Stat()
	b, _ := borrowed.Stat()
	if !os.SameFile(a, b) {
		t.Fatal("actual transfer did not enable verity; kernel used a copy")
	}
	assertNoParts(t, filepath.Join(dir, "cache"))
}
