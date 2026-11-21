//go:build linux

package slotlaunch

import (
	"context"
	"os"
	"path"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// This distinct class is old reserved18 plus the ACTUAL new Cold creator and
// its captured whole unit CG. It settles no incomplete13 from a status/ACK.
func (b *backend) abandonReservedStartV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("reserved abandonment needs its original native channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	if record.Slot == nil {
		return Response{}, refused("the original reserved allocation is not retained")
	}
	store, err := b.lock("store/.lock", false)
	if err != nil {
		return Response{}, err
	}
	defer store.Close()
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	roles, err := receiveCheckpointReadFilesV1(20)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range roles {
			file.Close()
		}
	}()
	if !pidExited(roles[18]) {
		return Response{}, refused("the actual new Cold creator has no positive native exit")
	}
	empty, err := originalSlotCgroupEmptyV2(roles[19])
	if err != nil || !empty {
		return Response{}, refused("the actual whole new Cold unit has no positive native vacancy")
	}
	if err := requireOriginalSlotActorsExitedV2(roles[:18]); err != nil {
		return Response{}, err
	}
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(*record.Slot), &allocation); err != nil {
		return Response{}, err
	}
	boot, err := nativeBootID()
	if err != nil || allocation.BootID != boot || allocation.Session != r.Session || allocation.UID != b.caller.uid || allocation.GID != b.caller.gid || allocation.Cgroup == nil {
		return Response{}, refused("the reserved abandonment source belongs to another native root allocation")
	}
	if err := b.checkSlotLeaseInodeV2(*record.Slot, roles[17]); err != nil {
		return Response{}, err
	}
	var old, newParent unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(roles[2].Fd()), &old) != nil || unix.Fstat(int(roles[19].Fd()), &newParent) != nil || unix.Fstatfs(int(roles[19].Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
		return Response{}, refused("the reserved abandonment has another native cgroup role")
	}
	oldAllocation := allocation.Cgroup.Dev == uint64(old.Dev) && allocation.Cgroup.Ino == old.Ino
	newAllocation := allocation.Cgroup.ParentDev == uint64(newParent.Dev) && allocation.Cgroup.ParentIno == newParent.Ino
	if !oldAllocation && !newAllocation {
		return Response{}, refused("the retained root allocation is neither the original reservation nor that actual new Cold unit")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	// The SAME EX reservation remains held. Restore only its protected
	// correlation to the actual retained original objects; clearing Slot
	// would lose that continuing reservation before a genuine retry.
	original, err := describeAbandonedReservationV2(roles[2], roles[13])
	if err != nil {
		return Response{}, err
	}
	allocation.Cgroup = &original
	if err := b.writeJSON(slotRecordName(*record.Slot), allocation); err != nil {
		return Response{}, err
	}
	if err := b.checkReservedSlotRolesV2(record, roles[:18]); err != nil {
		return Response{}, err
	}
	return checkpointNativeReplyV1(Response{OK: true})
}

// The native descriptor supplies correlation DATA even after its original
// cgroup was removed. No reconstructed path or record supplies Source or
// vacancy: both actual actors and SAME retained group were checked above.
func describeAbandonedReservationV2(group, parent *os.File) (cgroupIdentity, error) {
	var native, above unix.Stat_t
	for _, file := range []*os.File{group, parent} {
		var fs unix.Statfs_t
		if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC {
			return cgroupIdentity{}, refused("the continuing reservation has another native cgroup role")
		}
	}
	if unix.Fstat(int(group.Fd()), &native) != nil || unix.Fstat(int(parent.Fd()), &above) != nil || native.Mode&unix.S_IFMT != unix.S_IFDIR || above.Mode&unix.S_IFMT != unix.S_IFDIR || native.Uid != 0 {
		return cgroupIdentity{}, refused("the continuing reservation lost its original root cgroup identity")
	}
	link, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(group.Fd())))
	if err != nil {
		return cgroupIdentity{}, err
	}
	link = strings.TrimSuffix(link, " (deleted)")
	if !strings.HasPrefix(link, "/sys/fs/cgroup/") {
		return cgroupIdentity{}, refused("the original continuing reservation has no native cgroup correlation")
	}
	relative := strings.TrimPrefix(link, "/sys/fs/cgroup")
	if path.Clean(relative) != relative {
		return cgroupIdentity{}, refused("the native continuing reservation path is not canonical")
	}
	return cgroupIdentity{Path: relative, Dev: uint64(native.Dev), Ino: native.Ino, ParentDev: uint64(above.Dev), ParentIno: above.Ino}, nil
}
