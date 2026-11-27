//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// This distinct class has genuine NEW thirteen-role native creation plus
// SAME lease. It never relabels an old VMM peer as a new-start birth proof.
func (b *backend) cleanupReservedStartV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("reserved new-start cleanup needs its actual native channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	if record.Slot == nil {
		return Response{}, refused("the actual new reserved allocation is absent")
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
	roles, err := receiveCheckpointReadFilesV1(14)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range roles {
			file.Close()
		}
	}()
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(*record.Slot), &allocation); err != nil {
		return Response{}, err
	}
	boot, err := nativeBootID()
	if err != nil || allocation.BootID != boot || allocation.Session != r.Session || allocation.UID != b.caller.uid || allocation.Cgroup == nil {
		return Response{}, refused("the reserved cleanup has another actual new root allocation")
	}
	if err := b.checkSlotLeaseInodeV2(*record.Slot, roles[13]); err != nil {
		return Response{}, err
	}
	if !isNativePIDFD(roles[0]) || !isNativePIDFD(roles[1]) {
		return Response{}, refused("the new reserved keeper/init birth handles are incomplete")
	}
	var native unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(roles[2].Fd()), &native) != nil || unix.Fstatfs(int(roles[2].Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || uint64(native.Dev) != allocation.Cgroup.Dev || native.Ino != allocation.Cgroup.Ino {
		return Response{}, refused("the new reserved cleanup supplied another actual original group")
	}
	disk, err := b.open("vms/"+r.Session+"/disk.img", unix.O_RDONLY, 0)
	if err != nil {
		return Response{}, err
	}
	defer disk.Close()
	want, err := disk.Stat()
	if err != nil {
		return Response{}, err
	}
	got, err := roles[9].Stat()
	if err != nil || !os.SameFile(want, got) {
		return Response{}, refused("the new reserved cleanup disk changed identity")
	}
	empty, err := originalSlotCgroupEmptyV2(roles[2])
	if err != nil {
		return Response{}, err
	}
	if !empty {
		if err := killNativeCgroup(ctx, roles[2]); err != nil {
			return Response{}, err
		}
	}
	for {
		empty, err = originalSlotCgroupEmptyV2(roles[2])
		if err == nil && empty && pidExited(roles[0]) && pidExited(roles[1]) {
			break
		}
		if err := ctx.Err(); err != nil {
			return Response{}, errors.Join(err, refused("the actual new allocation closure remains unproved"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Op == "reserved.destroy-v2" {
		if err := b.root.RemoveAll("vms/" + r.Session); err != nil {
			return Response{}, err
		}
		if err := b.root.Remove("sessions/" + r.Session + ".sock"); err != nil && !os.IsNotExist(err) {
			return Response{}, err
		}
		if err := b.root.Remove("sessions/" + r.Session + ".json"); err != nil {
			return Response{}, err
		}
		if err := b.syncDir("sessions"); err != nil {
			return Response{}, err
		}
	}
	return checkpointNativeReplyV1(Response{OK: true})
}
