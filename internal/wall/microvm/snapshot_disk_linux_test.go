//go:build linux

package microvm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This is the real local descriptor/lease seam. It starts no VMM and grants no
// native binding: only NativeBindingV1's existing kernel owner can do that.
func snapshotDiskFixtureV1(t *testing.T) *snapshotDiskOwnerV1 {
	t.Helper()
	root, err := cachefs.OpenPrivateRoot(diskTestDir(t), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	if err := cachefs.MakePrivateDir(root, "vm"); err != nil {
		t.Fatal(err)
	}
	lease, err := acquireVMLeaseFor(t.Context(), root, cloudHypervisorBackend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lease.Close() })
	owner, err := pinSnapshotDiskRootsV1(root, lease, cloudHypervisorBackend, 4096)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.close() })
	file, err := cachefs.OpenFile(owner.vm, SessionDiskName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := errors.Join(file.Truncate(4096), file.Close()); err != nil {
		t.Fatal(err)
	}
	if err := owner.capture(t.Context()); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestSnapshotDiskUsesSameRunnerDescriptorAndAllowsLiveWrites(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	if _, err := owner.disk.WriteAt([]byte("live guest write"), 64); err != nil {
		t.Fatal(err)
	}
	if err := owner.borrow(t.Context(), func(session, vm *os.Root, disk *os.File) error {
		if session != owner.session || vm != owner.vm || disk != owner.disk {
			t.Fatal("the borrower received a replacement descriptor")
		}
		files := runnerFilesWithDiskV1(&bootFiles{}, disk, nil, nil)
		if len(files) != 9 || files[8] != disk || files[6] != nil || files[7] != nil {
			t.Fatal("the runner changed endpoint slots or reopened its disk")
		}
		return checkRunnerDiskV1(vm, files[8], 4096)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDiskRejectsSubstitutedFileAndDirectory(t *testing.T) {
	for _, changed := range []string{"disk", "directory", "size", "link"} {
		t.Run(changed, func(t *testing.T) {
			owner := snapshotDiskFixtureV1(t)
			switch changed {
			case "disk":
				if err := owner.vm.Rename(SessionDiskName, "saved.disk"); err != nil {
					t.Fatal(err)
				}
				file, err := cachefs.OpenFile(owner.vm, SessionDiskName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := errors.Join(file.Truncate(4096), file.Close()); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := owner.session.Rename("vm", "saved-vm"); err != nil {
					t.Fatal(err)
				}
				if err := owner.session.Mkdir("vm", 0o700); err != nil {
					t.Fatal(err)
				}
			case "size":
				if err := owner.disk.Truncate(8192); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := owner.vm.Link(SessionDiskName, "linked.disk"); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			if owner.borrow(t.Context(), func(*os.Root, *os.Root, *os.File) error { called = true; return nil }) == nil || called {
				t.Fatal("a changed original disk supplied a snapshot borrow")
			}
		})
	}
}

func TestSnapshotDiskCleanupJoinsOriginalBorrower(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	entered, release := make(chan struct{}), make(chan struct{})
	borrowed := make(chan error, 1)
	go func() {
		borrowed <- owner.borrow(t.Context(), func(*os.Root, *os.Root, *os.File) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- owner.close() }()
	select {
	case err := <-closed:
		close(release)
		t.Fatalf("cleanup released a borrowed original disk: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if _, err := owner.disk.Stat(); err != nil {
		close(release)
		t.Fatal("cleanup closed a descriptor still in its callback", err)
	}
	close(release)
	if err := <-borrowed; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := owner.borrow(context.Background(), func(*os.Root, *os.Root, *os.File) error { t.Fatal("closed owner borrowed again"); return nil }); err == nil {
		t.Fatal("cleanup did not fence a later borrow")
	}
}

func TestSnapshotDiskChecksOriginalAfterCallback(t *testing.T) {
	owner := snapshotDiskFixtureV1(t)
	err := owner.borrow(t.Context(), func(_ *os.Root, vm *os.Root, _ *os.File) error {
		return vm.Rename(SessionDiskName, "moved.disk")
	})
	if err == nil {
		t.Fatal("a callback that lost the current disk was reported as complete")
	}
}
