//go:build linux || darwin

package files

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// SpaceAt reads the filesystem of the actual pinned directory descriptor.
// A raced pathname or alias cannot select a different filesystem.
func SpaceAt(root *os.Root) (Space, error) {
	if root == nil {
		return Space{}, fmt.Errorf("filesystem root is missing")
	}
	file, err := root.Open(".")
	if err != nil {
		return Space{}, err
	}
	defer file.Close()
	var info unix.Statfs_t
	if err = unix.Fstatfs(int(file.Fd()), &info); err != nil {
		return Space{}, err
	}
	block := int64(info.Bsize)
	maximum := ^uint64(0)
	if block <= 0 || uint64(info.Blocks) > maximum/uint64(block) || uint64(info.Bavail) > maximum/uint64(block) {
		return Space{}, fmt.Errorf("filesystem space is invalid or overflows")
	}
	return Space{Available: uint64(info.Bavail) * uint64(block), Total: uint64(info.Blocks) * uint64(block), AllocationUnit: uint64(block)}, nil
}
