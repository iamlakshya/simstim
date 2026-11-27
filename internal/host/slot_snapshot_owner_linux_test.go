//go:build linux

package host

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSlotSnapshotOutsideRejectsUnissuedDataAndCopiedOwners(t *testing.T) {
	for _, incoming := range []*slotSnapshotIncomingV1{nil, {}, {used: true}} {
		if owner, err := acceptSlotSnapshotIncomingV1(context.Background(), incoming); err == nil || owner != nil {
			t.Fatal("data issued an actual outside Snapshot", owner, err)
		}
	}
	for _, incoming := range []*slotSnapshotReservedIncomingV2{nil, {}, {used: true}} {
		if owner, err := acceptSlotSnapshotReservedIncomingV2(context.Background(), incoming); err == nil || owner != nil {
			t.Fatal("data issued a reserved outside Snapshot", owner, err)
		}
	}
	var decoded SlotSnapshotOwnerV1
	if err := json.Unmarshal([]byte(`{}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err := decoded.beginV1(context.Background()); err == nil {
		t.Fatal("JSON issued an outside Snapshot")
	}
	original := &SlotSnapshotOwnerV1{}
	original.issuer = original
	copied := SlotSnapshotOwnerV1{issuer: original}
	if _, err := copied.beginV1(context.Background()); err == nil {
		t.Fatal("copied outside Snapshot issued a borrow")
	}
	selected := &slotSnapshotReadV1{}
	selected.issuer = selected
	copiedRead := slotSnapshotReadV1{issuer: selected}
	if _, err := copiedRead.beginV1(context.Background()); err == nil {
		t.Fatal("copied read owner issued a borrow")
	}
	if err := copiedRead.Close(); err == nil {
		t.Fatal("copied read owner released the original")
	}
}

func TestSlotSnapshotProcessRoleRequiresActualKernelPidfd(t *testing.T) {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatal(err)
	}
	actual := os.NewFile(uintptr(fd), "actual-snapshot-native-fixture")
	defer actual.Close()
	pid, err := slotSnapshotPIDV1(actual, true)
	if err != nil || pid != os.Getpid() {
		t.Fatal("actual live native role rejected", pid, err)
	}
	if _, err := slotSnapshotPIDV1(actual, false); err == nil {
		t.Fatal("live native role counted as positive physical exit")
	}
	ordinary, err := os.CreateTemp(t.TempDir(), "ordinary")
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Close()
	if _, err := slotSnapshotPIDV1(ordinary, true); err == nil {
		t.Fatal("ordinary file supplied a native process role")
	}
	if err := slotSnapshotGroupV1(ordinary); err == nil {
		t.Fatal("ordinary file supplied whole-group vacancy")
	}
}
