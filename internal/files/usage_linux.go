//go:build linux

package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

type diskInode struct {
	major, minor uint32
	inode        uint64
}

// diskStat is the actual no-follow metadata reader. Tests replace it only to
// inject a foreign mount; that is not a mount/namespace runtime proof.
var diskStat = func(directory int, name string, flags int) (unix.Statx_t, error) {
	var stat unix.Statx_t
	err := unix.Statx(directory, name, flags, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat)
	if err != nil {
		return stat, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mask&unix.STATX_BLOCKS == 0 || stat.Mask&unix.STATX_INO == 0 || stat.Mask&unix.STATX_TYPE == 0 {
		return stat, fmt.Errorf("filesystem allocation or mount identity is unavailable")
	}
	return stat, nil
}

// MeasureUsage measures only the actual factory's pinned owned writable roots.
// It never reads file payloads, follows links or enters another mount, even a
// same-device bind mount. Multiple owned roots deduplicate physical hardlinks.
// This is a non-atomic periodic measurement. An enumerated child which has
// disappeared beneath the same open parent may be skipped; known allocation
// may be retained conservatively. Other metadata failures never mean zero.
func MeasureUsage(ctx context.Context, roots ...*os.Root) (Usage, error) {
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}
	if len(roots) == 0 {
		return Usage{}, fmt.Errorf("disk accounting has no owned writable roots")
	}
	seen := map[diskInode]bool{}
	var usage Usage
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return Usage{}, err
		}
		if root == nil {
			return Usage{}, fmt.Errorf("disk accounting has no owned root")
		}
		directory, err := root.Open(".")
		if err != nil {
			return Usage{}, err
		}
		stat, err := diskStat(int(directory.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			err = walkDisk(ctx, directory, stat.Mnt_id, stat, seen, &usage)
		}
		err = errors.Join(err, directory.Close())
		if err != nil {
			return Usage{}, err
		}
	}
	return usage, ctx.Err()
}

func addDiskInode(stat unix.Statx_t, seen map[diskInode]bool, usage *Usage) error {
	key := diskInode{major: stat.Dev_major, minor: stat.Dev_minor, inode: stat.Ino}
	if seen[key] {
		return nil
	}
	bytes, err := allocationBytes(stat.Blocks)
	if err != nil || bytes > (1<<63-1)-usage.Allocated || usage.Inodes == 1<<63-1 {
		return ErrDiskOverflow
	}
	seen[key] = true
	usage.Allocated += bytes
	usage.Inodes++
	return nil
}

func walkDisk(ctx context.Context, directory *os.File, mount uint64, stat unix.Statx_t, seen map[diskInode]bool, usage *Usage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stat.Mnt_id != mount {
		return ErrDiskMount
	}
	key := diskInode{major: stat.Dev_major, minor: stat.Dev_minor, inode: stat.Ino}
	if seen[key] {
		return nil
	}
	if err := addDiskInode(stat, seen, usage); err != nil {
		return err
	}
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			child, err := diskStat(int(directory.Fd()), entry.Name(), unix.AT_SYMLINK_NOFOLLOW)
			if errors.Is(err, unix.ENOENT) {
				continue // this enumerated child disappeared; the parent stays pinned
			}
			if err != nil {
				return err
			}
			if child.Mnt_id != mount {
				return ErrDiskMount
			}
			if child.Mode&unix.S_IFMT != unix.S_IFDIR {
				if err = addDiskInode(child, seen, usage); err != nil {
					return err
				}
				continue
			}
			fd, err := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if errors.Is(err, unix.ENOENT) {
				// Its allocation was known before disappearance. Retain that
				// small conservative count; never substitute an ambient path.
				if err := addDiskInode(child, seen, usage); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			opened := os.NewFile(uintptr(fd), "owned-disk-directory")
			actual, err := diskStat(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
			if err == nil && actual.Mnt_id != mount {
				err = ErrDiskMount
			}
			if err == nil && (actual.Ino != child.Ino || actual.Dev_major != child.Dev_major || actual.Dev_minor != child.Dev_minor) {
				err = fmt.Errorf("disk directory changed while opening")
			}
			if err == nil {
				err = walkDisk(ctx, opened, mount, actual, seen, usage)
			}
			err = errors.Join(err, opened.Close())
			if err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
