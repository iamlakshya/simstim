package files

import "errors"

// ErrDiskMount is a refused crossing into another mount during accounting.
var ErrDiskMount = errors.New("disk accounting crossed an unowned mount")

// ErrDiskOverflow is a filesystem allocation count that cannot be represented.
var ErrDiskOverflow = errors.New("disk allocation count overflows")

// Usage is allocated filesystem storage, as du counts it. Directory, symlink
// and special inode storage is included without following link referents.
type Usage struct {
	Allocated int64
	Inodes    int64
}

func allocationBytes(blocks uint64) (int64, error) {
	const maximum = uint64(1<<63 - 1)
	if blocks > maximum/512 {
		return 0, ErrDiskOverflow
	}
	return int64(blocks * 512), nil
}
