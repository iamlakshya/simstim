//go:build linux

package files

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ValidateDiskRoots checks that every already-owned pinned writable root is on
// the same mount as the factory's pinned volume. It reads descriptor metadata
// only. It grants no root authority and does not establish path containment.
// The factory retains the volume and roots until every borrower has joined.
func ValidateDiskRoots(volume *os.Root, roots ...*os.Root) error {
	if len(roots) == 0 {
		return fmt.Errorf("disk accounting has no owned writable roots")
	}
	mount, err := pinnedDiskMount(volume)
	if err != nil {
		return fmt.Errorf("check the owned disk volume: %w", err)
	}
	for _, root := range roots {
		actual, err := pinnedDiskMount(root)
		if err != nil {
			return fmt.Errorf("check an owned writable root: %w", err)
		}
		if actual != mount {
			return ErrDiskMount
		}
	}
	return nil
}

func pinnedDiskMount(root *os.Root) (uint64, error) {
	if root == nil {
		return 0, fmt.Errorf("the pinned disk root is unavailable")
	}
	directory, err := root.Open(".")
	if err != nil {
		return 0, fmt.Errorf("open the pinned disk directory: %w", err)
	}
	stat, statErr := diskStat(int(directory.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
	if err := errors.Join(statErr, directory.Close()); err != nil {
		return 0, fmt.Errorf("read pinned disk mount metadata: %w", err)
	}
	return stat.Mnt_id, nil
}
