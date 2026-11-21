//go:build linux

package microvm

import (
	"errors"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func snapshotDiskPartialFixtureV1(t *testing.T) (*os.Root, *os.File) {
	t.Helper()
	root, err := cachefs.OpenPrivateRoot(diskTestDir(t), false)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := acquireVMLeaseFor(t.Context(), root, cloudHypervisorBackend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close(); _ = root.Close() })
	return root, lease
}

func TestSnapshotDiskPinFailureRetainsActualSessionRootOwner(t *testing.T) {
	root, lease := snapshotDiskPartialFixtureV1(t)
	owner, err := pinSnapshotDiskRootsV1(root, lease, cloudHypervisorBackend, 4096)
	if err == nil || owner == nil || owner.session == nil || owner.vm != nil {
		t.Fatal("missing-child abort lost its actual materialised session root", owner, err)
	}
	if _, err := owner.session.Stat("."); err != nil {
		t.Fatal("pin deferred-close retired the partial owner before its caller adopted it", err)
	}
	if owner.closing {
		t.Fatal("a partial pin attempted implicit cleanup before returning custody")
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal("a positively closed partial owner failed on repeated close", err)
	}
	if _, err := lease.Stat(); err != nil {
		t.Fatal("the partial owner closed its borrowed original runtime lease", err)
	}
}

func TestSnapshotDiskPinAdoptsChildRootBeforePrivateValidation(t *testing.T) {
	root, lease := snapshotDiskPartialFixtureV1(t)
	if err := root.Mkdir("vm", 0755); err != nil {
		t.Fatal(err)
	}
	owner, err := pinSnapshotDiskRootsV1(root, lease, cloudHypervisorBackend, 4096)
	if err == nil || owner == nil || owner.session == nil || owner.vm == nil {
		t.Fatal("invalid private child mode lost the actual opened child root", owner, err)
	}
	if _, err := owner.vm.Stat("."); err != nil {
		t.Fatal("the failed validation closed the adopted original child root", err)
	}
	called := false
	if err := owner.borrow(t.Context(), func(*os.Root, *os.Root, *os.File) error { called = true; return nil }); err == nil || called {
		t.Fatal("a partial root owner issued a native disk borrow")
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDiskCaptureRetainsActualDiskBeforeSizeAndModeValidation(t *testing.T) {
	for _, mode := range []os.FileMode{0600, 0644} {
		t.Run(map[os.FileMode]string{0600: "size", 0644: "mode"}[mode], func(t *testing.T) {
			root, lease := snapshotDiskPartialFixtureV1(t)
			if err := cachefs.MakePrivateDir(root, "vm"); err != nil {
				t.Fatal(err)
			}
			owner, err := pinSnapshotDiskRootsV1(root, lease, cloudHypervisorBackend, 4096)
			if err != nil {
				t.Fatal(err)
			}
			file, err := owner.vm.OpenFile(SessionDiskName, os.O_CREATE|os.O_EXCL|os.O_RDWR, mode)
			if err != nil {
				t.Fatal(err)
			}
			size := int64(4096)
			if mode == 0600 {
				size = 2048
			}
			if err := errors.Join(file.Truncate(size), file.Close()); err != nil {
				t.Fatal(err)
			}
			if err := owner.capture(t.Context()); err == nil || owner.disk == nil || owner.diskParent == nil {
				t.Fatal("capture lost its actual opened disk before validation", owner.disk, err)
			}
			if _, err := owner.disk.Stat(); err != nil {
				t.Fatal("validation directly closed the actual disk before abort ownership", err)
			}
			called := false
			if err := owner.borrow(t.Context(), func(*os.Root, *os.Root, *os.File) error { called = true; return nil }); err == nil || called {
				t.Fatal("a failed original disk capture issued a usable snapshot")
			}
			if err := owner.close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotDiskAbortCloseRetainsErrorAndActualObjectOnSecondClose(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	original := owner.disk
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	first := owner.close()
	if first == nil || !errors.Is(first, os.ErrClosed) {
		t.Fatal("abort lost the original disk close failure", first)
	}
	if owner.disk != original || owner.session == nil || owner.vm == nil || owner.diskParent == nil {
		t.Fatal("abort discarded its actual owned objects after a close error")
	}
	second := owner.close()
	if second == nil || !errors.Is(second, os.ErrClosed) {
		t.Fatal("a second close manufactured successful cleanup", second)
	}
	if _, err := owner.lease.Stat(); err != nil {
		t.Fatal("failed disk cleanup closed its borrowed runtime lease", err)
	}
}
