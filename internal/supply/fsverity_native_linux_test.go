//go:build linux

package supply

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

// This test performs real enable/measure ioctls ONLY when the validator has
// explicitly supplied an admitted writable fs-verity filesystem. It creates
// and removes its own fresh directory; it changes no filesystem feature.
func TestFSVerityNativeOwnedKernel(t *testing.T) {
	parent := os.Getenv("FSVERITY_NATIVE_FIXTURE")
	if parent == "" {
		t.Skip("no admitted native fs-verity fixture; no ioctl proof claimed")
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("native fixture must be an absolute admitted directory")
	}
	dir, err := os.MkdirTemp(parent, "verity-owned-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := []byte("fixed actual fs-verity kernel data fixture\n")
	it := NewItem(KindKernel, "guest", "6.18", runtime.GOARCH, data, base+KernelAsset(runtime.GOARCH))
	it.Licence, it.SourceURL, it.Released = "GPL-2.0-only", base+KernelSourceAsset(it.Version), "2026-09-28"
	path := filepath.Join(dir, "kernel")
	if err := os.WriteFile(path, data, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	enabled, err := InstallRuntimeVerity(context.Background(), it, source)
	if err != nil || !enabled {
		t.Fatal("admitted filesystem did not enable matching verity", enabled, err)
	}
	// Owner write permission must not defeat kernel-enforced immutability.
	if err := source.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	writer, writeErr := os.OpenFile(path, os.O_WRONLY, 0)
	if writer != nil {
		writer.Close()
	}
	if err := source.Chmod(0o444); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(writeErr, unix.EPERM) {
		t.Fatal("verity did not refuse a writable open despite owner write permission", writeErr)
	}
	v, err := PrepareRuntime(context.Background(), it, source)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	borrowed, err := v.File()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := source.Stat()
	b, _ := borrowed.Stat()
	if !v.verity || !os.SameFile(a, b) {
		t.Fatal("native verity kernel was copied instead of retaining its measured inode")
	}
	wrong := it
	wrong.FSVerity = hexA
	var mismatch *MismatchError
	if other, err := PrepareRuntime(context.Background(), wrong, source); !errors.As(err, &mismatch) {
		if other != nil {
			other.Close()
		}
		t.Fatal("native measured mismatch did not refuse", err)
	}
}
