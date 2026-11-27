//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestForkRootBorrowsTheOriginalDescriptorAfterItsPathIsReplaced(t *testing.T) {
	base := t.TempDir()
	originalPath := filepath.Join(base, "original")
	if err := os.MkdirAll(filepath.Join(originalPath, "work"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalPath, "work", "before"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if err := os.Rename(originalPath, filepath.Join(base, "retained")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(originalPath, "work"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(originalPath, "work", "before"), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	borrow, err := forkRootFromOriginalV1(original, "work")
	if err != nil {
		t.Fatal(err)
	}
	defer borrow.Close()
	data, err := borrow.ReadFile("before")
	if err != nil || string(data) != "original" {
		t.Fatalf("fork followed a replaced pathname: %q, %v", data, err)
	}
}

func TestForkRootsRefuseSymlinksAndMetadataOnlyOwners(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "elsewhere"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(base, "work")); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	if borrowed, err := forkRootFromOriginalV1(original, "work"); err == nil || borrowed != nil {
		t.Fatal("a planted symlink became a live fork root")
	}
	called := false
	for _, owner := range []*CopyOwnerV1{nil, {}, {native: &copyOwnerNativeV1{}}} {
		err := owner.WithForkRootsV1(t.Context(), func(*os.Root, *os.Root) error { called = true; return nil })
		if err == nil {
			t.Fatal("metadata-only copy owner issued live source roots")
		}
	}
	if called {
		t.Fatal("a callback ran without the original launcher")
	}
}

func TestForkRootBorrowCancellationDoesNotWaitForAnotherCopy(t *testing.T) {
	native := &copyOwnerNativeV1{}
	native.mu.Lock()
	defer native.mu.Unlock()
	owner := &CopyOwnerV1{native: native}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	err := owner.WithForkRootsV1(ctx, func(*os.Root, *os.Root) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled borrow entered another copy: called=%v, err=%v", called, err)
	}
}
