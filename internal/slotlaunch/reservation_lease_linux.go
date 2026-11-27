//go:build linux

package slotlaunch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// CheckOriginalReservationLeaseV2 observes only an existing root-issued EX
// FLOCK on this SAME open file description. It acquires or unlocks nothing
// and creates no Source, allocation, stop or restart permission.
func CheckOriginalReservationLeaseV2(file *os.File) error {
	if file == nil {
		return refused("the original slot reservation lease is absent")
	}
	info, err := file.Stat()
	if err != nil || !singleRegular(info, 0) || info.Mode().Perm() != 0o644 {
		return refused("the slot reservation is not its original root-owned lock file")
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return refused("the original slot reservation is not its read-only native description")
	}
	var native unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &native); err != nil {
		return err
	}
	fdinfo, err := os.Open(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return err
	}
	defer fdinfo.Close()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(fdinfo.Fd()), &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		return refused("the reservation lock observation is not native procfs")
	}
	raw, err := io.ReadAll(io.LimitReader(fdinfo, 4097))
	if err != nil || len(raw) > 4096 {
		return refused("the original reservation descriptor observation exceeds its bound")
	}
	identity := fmt.Sprintf("%02x:%02x:%d", unix.Major(uint64(native.Dev)), unix.Minor(uint64(native.Dev)), native.Ino)
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "lock:" {
			continue
		}
		if len(fields) != 9 || found || fields[1] != "1:" || fields[2] != "FLOCK" || fields[3] != "ADVISORY" || fields[4] != "WRITE" || fields[6] != identity || fields[7] != "0" || fields[8] != "EOF" {
			return refused("the original slot reservation has another native lock role")
		}
		pid, err := strconv.Atoi(fields[5])
		if err != nil || pid == 0 || pid < -1 {
			return refused("the existing slot lock has an invalid native locker observation")
		}
		found = true
	}
	if !found {
		return refused("the slot lease is an unlocked or independently reopened same-inode description")
	}
	return nil
}

func (b *backend) checkSlotLeaseInodeV2(slot int, file *os.File) error {
	if slot < 0 || slot >= SlotCount {
		return refused("the original reservation has no valid protected slot")
	}
	if err := CheckOriginalReservationLeaseV2(file); err != nil {
		return err
	}
	canonical, err := b.open(slotLockName(slot), unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer canonical.Close()
	want, err := canonical.Stat()
	if err != nil {
		return err
	}
	got, err := file.Stat()
	if err != nil || !os.SameFile(want, got) {
		return refused("the original reservation lease belongs to another root slot lock")
	}
	return nil
}

func originalSlotCgroupEmptyV2(group *os.File) (bool, error) {
	if group == nil {
		return false, refused("the original reserved cgroup descriptor is absent")
	}
	var info unix.Stat_t
	var fs unix.Statfs_t
	if err := unix.Fstat(int(group.Fd()), &info); err != nil {
		return false, err
	}
	if err := unix.Fstatfs(int(group.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return false, refused("the reserved source has no original cgroup2 directory")
	}
	if info.Nlink == 0 {
		return true, nil
	}
	live, err := populated(group)
	return !live, err
}

func requireOriginalSlotActorsExitedV2(roles []*os.File) error {
	if len(roles) < 18 {
		return refused("the original reserved native roles are incomplete")
	}
	if !pidExited(roles[0]) || !pidExited(roles[1]) || !pidExited(roles[14]) {
		return refused("the original reserved keeper, init or VMM has no positive native exit")
	}
	empty, err := originalSlotCgroupEmptyV2(roles[2])
	if err != nil || !empty {
		return errors.Join(err, refused("the original reserved whole cgroup has not proved vacancy"))
	}
	return nil
}
