//go:build linux

package slotlaunch

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestRootAllocationMatchesKeeperStrictWireFixture(t *testing.T) {
	record := slotRecord{Slot: 0, Session: "abc234de", UID: 1000, GID: 1000, SlotUID: 21000, SlotGID: 23000,
		BootID: "11111111-2222-3333-4444-555555555555",
		Cgroup: &cgroupIdentity{Path: "/unit/slot-00.fixture", Dev: 1, Ino: 2, ParentDev: 1, ParentIno: 3}}
	actual, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("../slotlease/testdata/root-allocation-parent-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, bytes.TrimSpace(expected)) {
		t.Fatalf("root record diverged from the keeper's actual strict wire fixture:\n%s\n%s", actual, expected)
	}
}
