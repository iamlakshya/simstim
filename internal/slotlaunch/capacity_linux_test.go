//go:build linux

package slotlaunch

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func capacityBackendV1(t *testing.T) (*backend, string) {
	t.Helper()
	b, dir := testBackend(t)
	b.caller.uid = 1000
	if err := os.MkdirAll(filepath.Join(dir, "root-steps/machine/slots/capacity"), 0o755); err != nil {
		t.Fatal(err)
	}
	return b, dir
}

func TestCapacityAtomicEmitReadAndUnknownHaveNoAllocationEffects(t *testing.T) {
	b, dir := capacityBackendV1(t)
	record, now := capacityFixtureV1()
	if err := b.writeCapacityLocked(record); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, CapacityProjectionRelativeV1(record.UID))
	original, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	record.Generation = strings.Repeat("b", 32)
	record.Counts = &CapacityCountsV1{Total: SlotCount, Occupied: 1, Free: SlotCount - 1}
	if err := b.writeCapacityLocked(record); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(original)
	if err != nil || !bytes.Contains(old, []byte(strings.Repeat("a", 32))) {
		t.Fatal("atomic replacement changed the previously opened observation")
	}
	if observed, known := b.readCapacity(record.UID, record.BootID, now); !known || observed.Generation != record.Generation || observed.Counts.Occupied != 1 {
		t.Fatal("reader did not receive the complete replacement generation")
	}
	record.Counts = nil
	if err := b.writeCapacityLocked(record); err != nil {
		t.Fatal(err)
	}
	if observed, known := b.readCapacity(record.UID, record.BootID, now); known || observed.Counts != nil {
		t.Fatal("fresh unknown projection became zero or infinite room")
	}
	for _, folder := range []string{"slots", "sessions", "vms"} {
		entries, err := os.ReadDir(filepath.Join(dir, folder))
		if err != nil || len(entries) != 0 {
			t.Fatal("capacity emission reserved or created session state")
		}
	}
}

func TestCapacityReadRefusesNativeOwnershipAndPathAliases(t *testing.T) {
	for _, kind := range []string{"missing", "foreign owner", "file mode", "symlink", "hardlink", "writable ancestor", "oversize", "foreign uid"} {
		t.Run(kind, func(t *testing.T) {
			b, dir := capacityBackendV1(t)
			record, now := capacityFixtureV1()
			if err := b.writeCapacityLocked(record); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, CapacityProjectionRelativeV1(record.UID))
			switch kind {
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "foreign owner":
				b.owner++
			case "file mode":
				if err := os.Chmod(name, 0o666); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(name, name+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(name)+".retained", name); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(name, name+".alias"); err != nil {
					t.Fatal(err)
				}
			case "writable ancestor":
				if err := os.Chmod(filepath.Join(dir, "root-steps/machine"), 0o777); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				if err := os.WriteFile(name, bytes.Repeat([]byte{' '}, CapacityProjectionMaxV1+1), 0o644); err != nil {
					t.Fatal(err)
				}
			case "foreign uid":
				record.UID++
				raw, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if observed, known := b.readCapacity(1000, record.BootID, now); known || observed.Counts != nil {
				t.Fatal("untrusted path or observation supplied slot room")
			}
		})
	}
}

func TestSlotCapacityPartitionsLeaseVacancyAndQuarantine(t *testing.T) {
	inventory := make([]Slot, SlotCount)
	for i := range inventory {
		inventory[i] = Slot{Number: i, Free: true}
	}
	inventory[0].Free = false
	inventory[1].Free, inventory[1].Quarantined = false, true
	counts := capacityCountsFromSlots(inventory)
	if !validCapacityCountsV1(counts) || counts.Occupied != 1 || counts.Quarantined != 1 || counts.Free != SlotCount-2 {
		t.Fatal("lease and quarantine partition became a whole-VM count")
	}
	inventory[0].Free = true
	counts = capacityCountsFromSlots(inventory)
	if counts.Occupied != 0 || counts.Free != SlotCount-1 || counts.Quarantined != 1 {
		t.Fatal("recovered native vacancy was not reflected in the advisory partition")
	}
	inventory[0].Number = 1
	if validCapacityCountsV1(capacityCountsFromSlots(inventory)) {
		t.Fatal("duplicate slot numbers supplied a known partition")
	}
}
