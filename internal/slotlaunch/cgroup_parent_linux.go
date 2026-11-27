//go:build linux

package slotlaunch

import (
	"os"

	"golang.org/x/sys/unix"
)

func cgroupParentStat(file *os.File) (unix.Stat_t, error) {
	var stat unix.Stat_t
	fd, err := unix.Openat(int(file.Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return stat, err
	}
	defer unix.Close(fd)
	err = unix.Fstat(fd, &stat)
	return stat, err
}

func checkCgroupParent(file *os.File, identity cgroupIdentity) error {
	if identity.ParentIno == 0 {
		return refused("the protected slot has no original native parent cgroup")
	}
	actual, err := cgroupParentStat(file)
	if err != nil {
		return err
	}
	if uint64(actual.Dev) != identity.ParentDev || actual.Ino != identity.ParentIno {
		return refused("the slot cgroup left its original native parent")
	}
	return nil
}
