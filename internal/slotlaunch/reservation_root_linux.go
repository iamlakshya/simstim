//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func (b *backend) reserveOriginalSlotV2(ctx context.Context, r Request, record *sessionRecord, allocation slotRecord, disk, keeper, init, peer, directory *os.File) (Response, error) {
	if err := b.checkCheckpointSourceV1(ctx, record, allocation, disk, keeper, init, peer); err != nil {
		return Response{}, err
	}
	if err := b.checkCheckpointDirectoryV1(r.Session, directory); err != nil {
		return Response{}, err
	}
	if err := b.checkOriginalKeeperV2(keeper, allocation); err != nil {
		return Response{}, err
	}
	// FD5 is fixed by the actual root keeper constructor. Neither its number
	// nor any process/path selector appears in request DATA.
	fd, err := unix.PidfdGetfd(int(keeper.Fd()), 5, 0)
	if err != nil {
		return Response{}, refused("the actual keeper's original slot lease cannot be duplicated by pidfd_getfd")
	}
	lease := os.NewFile(uintptr(fd), "original-keeper-issued-slot-lease")
	complete := false
	defer func() {
		if !complete {
			lease.Close()
		}
	}()
	if err := b.checkSlotLeaseInodeV2(allocation.Slot, lease); err != nil {
		return Response{}, err
	}
	if err := errors.Join(b.checkOriginalKeeperV2(keeper, allocation), b.checkCheckpointSourceV1(ctx, record, allocation, disk, keeper, init, peer)); err != nil {
		return Response{}, err
	}
	complete = true
	return checkpointNativeReplyV1(Response{OK: true}, lease)
}

func (b *backend) checkOriginalKeeperV2(keeper *os.File, allocation slotRecord) error {
	if !pidLive(keeper) {
		return refused("the original reservation keeper ended before its lease was captured")
	}
	pid, err := nativePID(keeper)
	if err != nil {
		return err
	}
	prefix := "/proc/" + strconv.Itoa(pid)
	executed, err := os.Open(prefix + "/exe")
	if err != nil {
		return err
	}
	defer executed.Close()
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer self.Close()
	got, err := executed.Stat()
	if err != nil {
		return err
	}
	want, err := self.Stat()
	if err != nil || !singleRegular(got, 0) || got.Mode().Perm() != 0o755 || !os.SameFile(got, want) {
		return refused("the actual keeper executes another root producer")
	}
	status, err := os.ReadFile(prefix + "/status")
	if err != nil || len(status) > RequestMax {
		return refused("the original keeper identity cannot be observed")
	}
	seenUID, seenGID := false, false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Uid:" && fields[0] != "Gid:" {
			continue
		}
		if len(fields) != 5 {
			return refused("the original keeper has malformed kernel credentials")
		}
		value := allocation.SlotUID
		if fields[0] == "Gid:" {
			value = allocation.SlotGID
			seenGID = true
		} else {
			seenUID = true
		}
		for _, field := range fields[1:] {
			if field != strconv.Itoa(value) {
				return refused("the actual keeper has another original slot identity")
			}
		}
	}
	if !seenUID || !seenGID {
		return refused("the original keeper identity is incomplete")
	}
	for _, name := range []string{"user", "pid", "cgroup"} {
		ours, err := os.Readlink("/proc/self/ns/" + name)
		if err != nil {
			return err
		}
		theirs, err := os.Readlink(prefix + "/ns/" + name)
		if err != nil || ours != theirs {
			return refused("the actual original keeper left its host namespaces")
		}
	}
	member, err := os.ReadFile(prefix + "/cgroup")
	if err != nil || len(member) > RequestMax {
		return refused("the actual keeper cgroup cannot be observed")
	}
	path, err := unifiedPath(string(member))
	if err != nil || allocation.Cgroup == nil || path != allocation.Cgroup.Path {
		return refused("the original keeper left its protected native allocation")
	}
	if !pidLive(keeper) {
		return refused("the original keeper ended during its lease inspection")
	}
	return nil
}

func (b *backend) stopCheckpointReservationV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("reserved stop needs its original native descriptor channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
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
	roles, err := receiveCheckpointReadFilesV1(18)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range roles {
			file.Close()
		}
	}()
	if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
		return Response{}, err
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
		if err := requireOriginalSlotActorsExitedV2(roles); err == nil {
			break
		}
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
		return Response{}, err
	}
	// The lease deliberately remains EX on this SAME OFD. Do not enter the
	// ordinary fresh-open recovery or clear its allocation before restart.
	return checkpointNativeReplyV1(Response{OK: true})
}

func (b *backend) checkReservedSlotRolesV2(record *sessionRecord, roles []*os.File) error {
	if err := b.checkReservedSlotCoreV2(record, roles); err != nil {
		return err
	}
	current, err := b.open("vms/"+record.Session+"/disk.img", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer current.Close()
	want, err := current.Stat()
	if err != nil {
		return err
	}
	got, err := roles[9].Stat()
	if err != nil || !os.SameFile(want, got) || !singleRegular(want, record.SlotUID) || want.Mode().Perm() != 0o660 {
		return refused("the reserved original disk changed native identity")
	}
	return nil
}

func (b *backend) checkReservedSlotCoreV2(record *sessionRecord, roles []*os.File) error {
	if record == nil || record.Slot == nil || len(roles) < 18 {
		return refused("the reserved operation has no original protected allocation")
	}
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(*record.Slot), &allocation); err != nil {
		return err
	}
	boot, err := nativeBootID()
	if err != nil || allocation.Session != record.Session || allocation.UID != b.caller.uid || allocation.GID != b.caller.gid || allocation.BootID != boot || allocation.Cgroup == nil {
		return refused("the reserved operation belongs to another original root allocation")
	}
	if err := b.checkSlotLeaseInodeV2(allocation.Slot, roles[17]); err != nil {
		return err
	}
	for _, index := range []int{0, 1, 14} {
		if !isNativePIDFD(roles[index]) {
			return refused("a reserved original birth capability has another native role")
		}
	}
	var group, parent unix.Stat_t
	var fs unix.Statfs_t
	if unix.Fstat(int(roles[2].Fd()), &group) != nil || unix.Fstatfs(int(roles[2].Fd()), &fs) != nil || fs.Type != unix.CGROUP2_SUPER_MAGIC || uint64(group.Dev) != allocation.Cgroup.Dev || group.Ino != allocation.Cgroup.Ino || unix.Fstat(int(roles[13].Fd()), &parent) != nil || uint64(parent.Dev) != allocation.Cgroup.ParentDev || parent.Ino != allocation.Cgroup.ParentIno {
		return refused("the reserved operation supplied another actual original cgroup or parent")
	}
	if err := b.checkCheckpointDirectoryV1(record.Session, roles[16]); err != nil {
		return err
	}
	return nil
}
