//go:build linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/proto"
	"testing"
)

func TestSnapshotReaderPurposesRemainDistinct(t *testing.T) {
	line := &StartupLineV1{loanFiles: 1}
	if err := line.armSlotSnapshotV1(); err != nil {
		t.Fatal(err)
	}
	if err := line.armSlotSnapshotReservationV2(); err == nil {
		t.Fatal("legacy17 became reserved18")
	}
	if _, _, err := line.takeSlotSnapshotReservationV2(); err == nil {
		t.Fatal("legacy reader produced a reserved allocation Source")
	}
	if err := line.finishSlotSnapshotV1(); err != nil {
		t.Fatal(err)
	}
	if err := line.armSlotSnapshotReservationV2(); err != nil {
		t.Fatal(err)
	}
	if err := line.armSlotSnapshotV1(); err == nil {
		t.Fatal("reserved18 entered legacy17 purpose")
	}
	if _, _, err := line.takeSlotSnapshotV1(); err == nil {
		t.Fatal("reserved reader produced a legacy actual loan without roles")
	}
	if err := line.finishSlotSnapshotReservationV2(); err != nil {
		t.Fatal(err)
	}
	line.slotStartupPending = &slotReservedStartupPendingV2{}
	if err := line.armSlotSnapshotV1(); err == nil {
		t.Fatal("startup19 armed legacy17")
	}
	if err := line.armSlotSnapshotReservationV2(); err == nil {
		t.Fatal("startup19 armed reserved18")
	}
	if _, _, err := line.takeSlotReservedStartupV2(); err == nil {
		t.Fatal("pending startup DATA produced native OP19")
	}
	if line.loanFiles != 1 {
		t.Fatal("closed purposes widened ordinary startup OP1")
	}
}

func TestReservedIncomingDTOsCannotIssueNativeSource(t *testing.T) {
	for _, incoming := range []*slotSnapshotReservedIncomingV2{nil, {}, {state: proto.NativeStopStateV1{Version: 1, Primary: "parent23"}}} {
		if err := incoming.checkOriginalV2(t.Context()); err == nil {
			t.Fatal("a reserved DTO created actual original custody")
		}
	}
	for _, incoming := range []*slotReservedStartupIncomingV2{nil, {}, {preparation: &slotReservedStartupPendingV2{prepare: slotReservedStartupPrepareV2{Version: 2, Primary: "parent23"}}}} {
		if err := incoming.checkOriginalV2(context.Background()); err == nil {
			t.Fatal("a startup DTO created actual original boot custody")
		}
	}
}
