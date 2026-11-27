//go:build linux

package files

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskRootsRequireSamePinnedVolumeMount(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "owned"), 0700); err != nil {
		t.Fatal(err)
	}
	volume, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close()
	root, err := volume.OpenRoot("owned")
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := ValidateDiskRoots(volume, root, root); err != nil {
		t.Fatalf("same pinned volume refused: %v", err)
	}
	info, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	inode := info.Sys().(*syscall.Stat_t).Ino
	original := diskStat
	defer func() { diskStat = original }()
	calls := 0
	diskStat = func(fd int, name string, flags int) (unix.Statx_t, error) {
		calls++
		if name != "" || flags != unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW {
			t.Error("mount validation enumerated paths or followed links")
		}
		stat, err := original(fd, name, flags)
		if stat.Ino == inode {
			stat.Mnt_id++ // same device/inode at the root, a different mount
		}
		return stat, err
	}
	if err := ValidateDiskRoots(volume, root); !errors.Is(err, ErrDiskMount) || calls != 2 {
		t.Fatalf("foreign top-level mount accepted: calls=%d err=%v", calls, err)
	}
}

func TestDiskRootsUnavailableMetadataNeverMeansSameVolume(t *testing.T) {
	volume, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer volume.Close()
	if err := ValidateDiskRoots(nil, volume); err == nil {
		t.Fatal("nil volume accepted")
	}
	if err := ValidateDiskRoots(volume); err == nil {
		t.Fatal("empty root inventory accepted")
	}
	if err := ValidateDiskRoots(volume, nil); err == nil {
		t.Fatal("unknown owned root accepted")
	}
	closed, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if err := ValidateDiskRoots(volume, closed); err == nil {
		t.Fatal("closed root accepted")
	}
	original := diskStat
	defer func() { diskStat = original }()
	for _, failAt := range []int{1, 2} {
		calls := 0
		diskStat = func(fd int, name string, flags int) (unix.Statx_t, error) {
			calls++
			if calls == failAt {
				return unix.Statx_t{}, syscall.EIO
			}
			return original(fd, name, flags)
		}
		if err := ValidateDiskRoots(volume, volume); !errors.Is(err, syscall.EIO) {
			t.Fatalf("mount read failure %d accepted: %v", failAt, err)
		}
	}
}
