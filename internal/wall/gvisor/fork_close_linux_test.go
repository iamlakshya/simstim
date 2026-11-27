//go:build linux

package gvisor

import (
	"errors"
	"os"
	"testing"
)

func TestForkMountCloseRetainsFailureAndOriginalHandles(t *testing.T) {
	failed, err := os.CreateTemp(t.TempDir(), "closed-mount-")
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := os.CreateTemp(t.TempDir(), "held-mount-")
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Close(); err != nil {
		t.Fatal(err)
	}
	// Resource closure alone cannot construct a mounted-root producer.
	owner := &ForkMountsV1{files: []*os.File{failed, remaining}}
	for i := 0; i < 2; i++ {
		if err := owner.close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("close %d forgot the actual descriptor failure: %v", i, err)
		}
		if owner.closed || !owner.closing || len(owner.files) != 2 || owner.files[0] != failed || owner.files[1] != remaining {
			t.Fatal("a failed close discarded original objects or reported complete closure")
		}
	}
	if _, err := remaining.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("closure did not join the remaining original handle", err)
	}
}

func TestFreshHomeCloseFailureAfterActualRemovalStaysFailure(t *testing.T) {
	storage := freshHomeStorageFixtureV1(t)
	home, err := createFreshForkHomeV1(storage)
	if err != nil {
		t.Fatal(err)
	}
	if err := home.check(); err != nil {
		t.Fatal(err)
	}
	if err := storage.root.RemoveAll(home.name); err != nil {
		t.Fatal(err)
	}
	home.removed = true // The actual physical removal above succeeded.
	if err := home.file.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := home.close(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("close %d forgot the actual home descriptor failure: %v", i, err)
		}
		if home.closed || !home.removed || home.file == nil || home.root == nil {
			t.Fatal("a failed close lost the original removal progress or handles")
		}
	}
}
