//go:build linux

package guest

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func prepareSelfTestFS() error {
	for _, m := range []struct {
		src, target, fstype string
		flags               uintptr
	}{
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID},
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
	} {
		if err := unix.Mount(m.src, m.target, m.fstype, m.flags, ""); err != nil && !errors.Is(err, syscall.EBUSY) {
			return fmt.Errorf("mount %s: %w", m.target, err)
		}
	}
	return nil
}

func statRootDev() (uint32, uint32, error) {
	var st unix.Stat_t
	if err := unix.Stat("/", &st); err != nil {
		return 0, 0, err
	}
	dev := uint64(st.Dev) //nolint:unconvert // Dev is uint64 on 64-bit Linux
	return unix.Major(dev), unix.Minor(dev), nil
}

func statDM0BlockDev() (uint32, uint32, error) {
	var st unix.Stat_t
	if err := unix.Stat("/dev/dm-0", &st); err != nil {
		return 0, 0, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFBLK {
		return 0, 0, errors.New("/dev/dm-0 is not a block device")
	}
	rdev := uint64(st.Rdev) //nolint:unconvert // Rdev is uint64 on 64-bit Linux
	return unix.Major(rdev), unix.Minor(rdev), nil
}

func powerOffGuest() error {
	unix.Sync()
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
