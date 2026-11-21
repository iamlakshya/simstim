//go:build linux

package slotlease

import (
	"golang.org/x/sys/unix"
	"os"
)

// This is a fresh native check of the parent reached from the SAME original
// cgroup object. The root record supplies comparison DATA, never authority
// to adopt a different group, keeper, allocation, process or namespace.
func checkCgroupParent(file *os.File, identity cgroupIdentity) error {
	if file == nil || identity.ParentDev == 0 || identity.ParentIno == 0 {
		return failure("the slot allocation has no original native parent cgroup")
	}
	fd, err := unix.Openat(int(file.Fd()), "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var actual unix.Stat_t
	if err := unix.Fstat(fd, &actual); err != nil {
		return err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return failure("the original parent is not a native cgroup2 object")
	}
	if actual.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(actual.Dev) != identity.ParentDev || actual.Ino != identity.ParentIno || uint64(actual.Dev) != identity.Dev {
		return failure("the original native slot cgroup parent changed")
	}
	return nil
}
