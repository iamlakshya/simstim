//go:build linux

package slotlaunch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDestroyConfirmsTrueAbsenceAndAllowsOwnedRetry(t *testing.T) {
	b, dir := testBackend(t)
	const id = "abc234de"
	if err := b.destroy(t.Context(), id); err != nil {
		t.Fatal("never-started preparation cannot be removed:", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "sessions", id+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("absence confirmation created ownership")
	}
	if err := os.Mkdir(filepath.Join(dir, "vms", id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vms", id, "disk.img"), []byte("owned disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := sessionRecord{Session: id, UID: b.caller.uid, GID: b.caller.gid}
	if err := b.writeJSON("sessions/"+id+".json", record); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := b.destroy(t.Context(), id); err != nil {
			t.Fatal("owned root cleanup or retry failed:", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "vms", id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned VM directory remains")
	}
}

func TestDestroyAbsentRecordPreservesEveryOrphan(t *testing.T) {
	for _, kind := range []string{"VM directory", "VM file", "VM symlink", "socket file", "socket symlink"} {
		t.Run(kind, func(t *testing.T) {
			b, dir := testBackend(t)
			name := filepath.Join(dir, "vms", "abc234de")
			if kind == "socket file" || kind == "socket symlink" {
				name = filepath.Join(dir, "sessions", "abc234de.sock")
			}
			var err error
			switch kind {
			case "VM directory":
				err = os.Mkdir(name, 0o700)
			case "VM symlink", "socket symlink":
				err = os.Symlink("missing-target", name)
			default:
				err = os.WriteFile(name, []byte("unowned object"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.destroy(t.Context(), "abc234de"); err == nil {
				t.Fatal("orphan was treated as an absent root session")
			}
			after, err := os.Lstat(name)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("orphan was deleted or replaced")
			}
		})
	}
}

func TestDestroyForeignRecordRetainsOwnedTree(t *testing.T) {
	b, dir := testBackend(t)
	const id = "abc234de"
	if err := os.Mkdir(filepath.Join(dir, "vms", id), 0o700); err != nil {
		t.Fatal(err)
	}
	record := sessionRecord{Session: id, UID: b.caller.uid + 1, GID: b.caller.gid}
	if err := b.writeJSON("sessions/"+id+".json", record); err != nil {
		t.Fatal(err)
	}
	if err := b.destroy(t.Context(), id); err == nil {
		t.Fatal("foreign session was destroyed")
	}
	if _, err := os.Stat(filepath.Join(dir, "vms", id)); err != nil {
		t.Fatal("foreign VM directory was changed:", err)
	}
	var retained sessionRecord
	if err := b.readJSON("sessions/"+id+".json", &retained); err != nil || retained.UID != record.UID {
		t.Fatal("foreign session record was changed:", err)
	}
}

func TestPreparationCannotAdoptAnUnrecordedRootTree(t *testing.T) {
	b, dir := testBackend(t)
	const id = "abc234de"
	if err := os.Mkdir(filepath.Join(dir, "vms", id), 0o700); err != nil {
		t.Fatal(err)
	}
	record := sessionRecord{Session: id, UID: b.caller.uid, GID: b.caller.gid}
	if err := b.recordVMPreparation(&record, b.caller.uid, nil); err == nil {
		t.Fatal("a new ownership record adopted an existing orphan")
	}
	if _, err := os.Lstat(filepath.Join(dir, "sessions", id+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("orphan refusal wrote caller ownership")
	}
}

func TestPreparationRecordFailureCreatesNoMutableTree(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the write-refusal fixture needs an ordinary account")
	}
	b, dir := testBackend(t)
	sessions := filepath.Join(dir, "sessions")
	if err := os.Chmod(sessions, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(sessions, 0o700)
	record := sessionRecord{Session: "abc234de", UID: b.caller.uid, GID: b.caller.gid}
	if err := b.recordVMPreparation(&record, b.caller.uid, nil); err == nil {
		t.Fatal("failed protected record publication was accepted")
	}
	if _, err := os.Lstat(filepath.Join(dir, "vms", record.Session)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("record publication failure created a mutable tree")
	}
}

func TestRecordedEarlyDiskFailureRemainsOwnerCleanable(t *testing.T) {
	b, dir := testBackend(t)
	r := validStart()
	r.DiskMaxGiB = 1
	record := sessionRecord{Session: r.Session, UID: b.caller.uid, GID: b.caller.gid}
	if err := b.recordVMPreparation(&record, b.caller.uid, r.digests()); err != nil {
		t.Fatal(err)
	}
	folder, disk, fresh, err := b.prepareVM(r, b.caller.uid)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("early disk fixture was not fresh")
	}
	// Simulate a failed formatter without starting any formatter or jail.
	if err := b.removeCapturedFile(disk.Name(), disk); err != nil {
		t.Fatal(err)
	}
	disk.Close()
	folder.Close()
	for range 2 {
		if err := b.destroy(t.Context(), r.Session); err != nil {
			t.Fatal("recorded early failure or cleanup retry failed:", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dir, "vms", r.Session)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("early failed disk tree remains")
	}
}

func TestRecoveryRetainsExclusiveLeaseAndNeverAdoptsReplacement(t *testing.T) {
	b, _ := testBackend(t)
	slot := 0
	record := sessionRecord{Session: "abc234de", UID: b.caller.uid, GID: b.caller.gid, Slot: &slot}
	old := slotRecord{Slot: slot, Session: record.Session, UID: record.UID, GID: record.GID, Cgroup: &cgroupIdentity{Path: "/old", Dev: 1, Ino: 2, ParentDev: 1, ParentIno: 3}}
	replacement := old
	replacement.Session = "abc234df"
	replacement.Cgroup = &cgroupIdentity{Path: "/new", Dev: 1, Ino: 4, ParentDev: 1, ParentIno: 3}
	if err := b.writeJSON(slotRecordName(slot), replacement); err != nil {
		t.Fatal(err)
	}
	lease, err := b.lock(slotLockName(slot), true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := b.recoverAllocatedCgroup(t.Context(), &record, old, lease); err != nil {
		t.Fatal(err)
	}
	if record.Slot != nil {
		t.Fatal("stale session kept a replacement allocation")
	}
	if other, err := b.lock(slotLockName(slot), true); !errors.Is(err, unix.EWOULDBLOCK) {
		if other != nil {
			other.Close()
		}
		t.Fatal("recovery released its caller's exclusive lease")
	}
	var current slotRecord
	if err := b.readJSON(slotRecordName(slot), &current); err != nil || !sameSlotAllocation(replacement, current) {
		t.Fatal("recovery changed the replacement allocation:", err)
	}
}

func TestRecoveryChangedNativeBootSettlesOnlyWithExclusiveLease(t *testing.T) {
	b, _ := testBackend(t)
	boot, err := nativeBootID()
	if err != nil {
		t.Fatal(err)
	}
	oldBoot := "0" + boot[1:]
	if oldBoot == boot {
		oldBoot = "1" + boot[1:]
	}
	slot := 0
	record := sessionRecord{Session: "abc234de", UID: b.caller.uid, GID: b.caller.gid, Slot: &slot}
	old := slotRecord{Slot: slot, Session: record.Session, UID: record.UID, GID: record.GID, BootID: oldBoot,
		Cgroup: &cgroupIdentity{Path: "/unavailable-old-boot-cgroup", Dev: 1, Ino: 2}}
	if err := b.writeJSON(slotRecordName(slot), old); err != nil {
		t.Fatal(err)
	}
	lease, err := b.lock(slotLockName(slot), true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := b.recoverAllocatedCgroup(t.Context(), &record, old, lease); err != nil {
		t.Fatal("changed boot attempted to reopen the unavailable old cgroup:", err)
	}
	if record.Slot != nil {
		t.Fatal("changed boot retained an impossible old process association")
	}
	var retained slotRecord
	if err := b.readJSON(slotRecordName(slot), &retained); err != nil || !sameSlotAllocation(old, retained) {
		t.Fatal("changed-boot settlement changed or adopted the slot record")
	}
}

func TestCgroupParentUsesCapturedNativeObject(t *testing.T) {
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	for _, name := range []string{first, second, filepath.Join(first, "child")} {
		if err := os.Mkdir(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	child, err := os.Open(filepath.Join(first, "child"))
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	parent, err := cgroupParentStat(child)
	if err != nil {
		t.Fatal(err)
	}
	identity := cgroupIdentity{ParentDev: uint64(parent.Dev), ParentIno: parent.Ino}
	if err := checkCgroupParent(child, identity); err != nil {
		t.Fatal(err)
	}
	if err := checkCgroupParent(child, cgroupIdentity{}); err == nil {
		t.Fatal("old record with no original parent identity was accepted")
	}
	if err := os.Rename(filepath.Join(first, "child"), filepath.Join(second, "child")); err != nil {
		t.Fatal(err)
	}
	if err := checkCgroupParent(child, identity); err == nil {
		t.Fatal("a moved native child retained its former parent identity")
	}
}
