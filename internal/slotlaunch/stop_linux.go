//go:build linux

package slotlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func (b *backend) kill(ctx context.Context, id string) error {
	record, lock, err := b.session(id, false)
	if err != nil {
		return err
	}
	defer lock.Close()
	return b.killLocked(ctx, record)
}

func (b *backend) clearSessionSlot(record *sessionRecord) error {
	record.Slot = nil
	return b.writeJSON("sessions/"+record.Session+".json", record)
}

func (b *backend) killLocked(ctx context.Context, record *sessionRecord) error {
	// Start takes this same lock before a slot lease. Retain that order so
	// no replacement allocation can race recovery of the captured cgroup.
	storeLock, err := b.lock("store/.lock", false)
	if err != nil {
		return err
	}
	defer storeLock.Close()
	defer func() { _ = b.refreshCapacityLocked() }()
	return b.killAllocatedLocked(ctx, record)
}

func (b *backend) killAllocatedLocked(ctx context.Context, record *sessionRecord) error {
	if record.Slot == nil {
		return nil
	}
	slot := *record.Slot
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(slot), &allocation); err != nil {
		return err
	}
	if allocation.Session != record.Session {
		return b.clearSessionSlot(record)
	}
	slotLock, err := b.lock(slotLockName(slot), true)
	if err == nil {
		defer slotLock.Close()
		return b.recoverAllocatedCgroup(ctx, record, allocation, slotLock)
	}
	if !errors.Is(err, unix.EWOULDBLOCK) {
		return err
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", RootPath()+"/sessions/"+record.Session+".sock")
	if err != nil {
		return b.recoverAllocatedCgroup(ctx, record, allocation, nil)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(30 * time.Second))
	if err := json.NewEncoder(connection).Encode(Request{Op: "kill", Session: record.Session}); err != nil {
		return err
	}
	var response Response
	if json.NewDecoder(io.LimitReader(connection, RequestMax)).Decode(&response) != nil || !response.OK {
		return refused("the keeper has not proved native process closure")
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		slotLock, err = b.lock(slotLockName(slot), true)
		if err == nil {
			defer slotLock.Close()
			return b.recoverAllocatedCgroup(ctx, record, allocation, slotLock)
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return refused("the stopped keeper still holds its slot lease")
}

func sameSlotAllocation(left, right slotRecord) bool {
	return left.Slot == right.Slot && left.Session == right.Session && left.UID == right.UID && left.GID == right.GID &&
		left.SlotUID == right.SlotUID && left.SlotGID == right.SlotGID && left.BootID == right.BootID &&
		left.Cgroup != nil && right.Cgroup != nil && *left.Cgroup == *right.Cgroup
}

// recoverAllocatedCgroup holds the store lock and, when supplied, the fresh
// exclusive slot lease. A free lease alone is not process closure: an escaped
// descendant can outlive both keeper and namespace init.
func (b *backend) recoverAllocatedCgroup(ctx context.Context, record *sessionRecord, allocation slotRecord, lease *os.File) error {
	var current slotRecord
	if err := b.readJSON(slotRecordName(allocation.Slot), &current); err != nil {
		return err
	}
	if !sameSlotAllocation(allocation, current) {
		return b.clearSessionSlot(record)
	}
	boot, err := nativeBootID()
	if err != nil {
		return err
	}
	if record.Slot == nil || allocation.Slot != *record.Slot || allocation.Session != record.Session || allocation.UID != b.caller.uid || allocation.GID != b.caller.gid || !bootIDPattern.MatchString(allocation.BootID) || allocation.Cgroup == nil {
		return refused("the lost keeper has no same-boot original root allocation")
	}
	// A positive change of the native boot UUID proves that no process from
	// the old allocation survives. Settle only under the fresh exclusive
	// lease, without opening or adopting any cgroup at its previous path.
	if allocation.BootID != boot {
		return b.finishRecoveredAllocation(record, allocation, lease)
	}
	root, err := cgroupRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	group, err := openCgroup(root, *allocation.Cgroup)
	if err != nil {
		return err
	}
	defer group.Close()
	live, err := populated(group)
	if err != nil {
		return err
	}
	if live {
		if err := killNativeCgroup(ctx, group); err != nil {
			return err
		}
	}
	return b.finishRecoveredAllocation(record, allocation, lease)
}

func (b *backend) finishRecoveredAllocation(record *sessionRecord, allocation slotRecord, lease *os.File) error {
	var current slotRecord
	var err error
	if lease == nil {
		lease, err = b.lock(slotLockName(allocation.Slot), true)
		if err != nil {
			return refused("the stopped original cgroup still has an uncertain slot lease")
		}
		defer lease.Close()
	}
	// Re-read only after obtaining the exclusive lease. Never clear or kill
	// a newer generation merely because it reuses this slot number.
	if err := b.readJSON(slotRecordName(allocation.Slot), &current); err != nil {
		return err
	}
	if !sameSlotAllocation(allocation, current) {
		return b.clearSessionSlot(record)
	}
	if err := b.vacancy(allocation.Slot); err != nil {
		return err
	}
	return b.clearSessionSlot(record)
}

func (b *backend) destroy(ctx context.Context, id string) error {
	// The stable protected lock also covers a never-started preparation and
	// retries after root cleanup succeeded but local cleanup was interrupted.
	lock, err := b.lock("sessions/"+id+".lock", false)
	if err != nil {
		return err
	}
	defer lock.Close()
	storeLock, err := b.lock("store/.lock", false)
	if err != nil {
		return err
	}
	defer storeLock.Close()
	defer func() { _ = b.refreshCapacityLocked() }()
	var record sessionRecord
	if err := b.readJSON("sessions/"+id+".json", &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return b.confirmSessionAbsent(id)
		}
		return err
	}
	if record.Session != id || record.UID != b.caller.uid || record.GID != b.caller.gid {
		return refused("this session belongs to another login account")
	}
	if record.Slot != nil && (*record.Slot < 0 || *record.Slot >= SlotCount) {
		return refused("the protected session record contains an invalid slot")
	}
	if err := b.killAllocatedLocked(ctx, &record); err != nil {
		return err
	}
	// os.Root.RemoveAll never follows links inside the caller-owned VM tree.
	if err := b.root.RemoveAll("vms/" + id); err != nil {
		return err
	}
	if err := b.root.Remove("sessions/" + id + ".sock"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := b.root.Remove("sessions/" + id + ".json"); err != nil {
		return err
	}
	return b.syncDir("sessions")
}

func (b *backend) confirmSessionAbsent(id string) error {
	for _, name := range []string{"vms/" + id, "sessions/" + id + ".sock"} {
		if _, err := b.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return refused("an unowned root session object remains; keep it for inspection")
		}
	}
	return nil
}
