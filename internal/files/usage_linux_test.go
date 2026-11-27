//go:build linux

package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestAllocatedUsageSparseHardlinksAndUnfollowedSymlink(t *testing.T) {
	directory := t.TempDir()
	sparse, err := os.OpenFile(filepath.Join(directory, "sparse"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = sparse.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	if _, err = sparse.WriteAt([]byte("x"), 1<<30-1); err != nil {
		t.Fatal(err)
	}
	sparse.Close()
	if err = os.Link(filepath.Join(directory, "sparse"), filepath.Join(directory, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "regular"), []byte("allocated"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, make([]byte, 1<<20), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	usage, err := MeasureUsage(t.Context(), root, root)
	if err != nil {
		t.Fatal(err)
	}
	var wanted int64
	for _, name := range []string{".", "sparse", "regular", "alias"} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		native := info.Sys().(*syscall.Stat_t)
		wanted += native.Blocks * 512
	}
	if usage.Allocated != wanted || usage.Inodes != 4 || usage.Allocated >= 1<<30 {
		t.Fatalf("usage = %+v, wanted allocated %d", usage, wanted)
	}
}

func TestSameDeviceForeignMountRefusesBeforeEnumeration(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "foreign"), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original := diskStat
	defer func() { diskStat = original }()
	diskStat = func(fd int, name string, flags int) (unix.Statx_t, error) {
		info, err := original(fd, name, flags)
		if err == nil && name == "foreign" {
			info.Mnt_id++ // same dev/inode, different mount
		}
		return info, err
	}
	if _, err = MeasureUsage(t.Context(), root); !errors.Is(err, ErrDiskMount) {
		t.Fatalf("foreign mount = %v", err)
	}
}

func TestAllocationOverflowAndMeasurementFailureNeverLookWithinBudget(t *testing.T) {
	if _, err := allocationBytes(^uint64(0)); !errors.Is(err, ErrDiskOverflow) {
		t.Fatal("allocation overflow accepted")
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original := diskStat
	defer func() { diskStat = original }()
	diskStat = func(int, string, int) (unix.Statx_t, error) { return unix.Statx_t{}, syscall.EIO }
	if usage, err := MeasureUsage(t.Context(), root); err == nil || usage != (Usage{}) {
		t.Fatal("failed measurement became an invented successful total")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := MeasureUsage(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}
