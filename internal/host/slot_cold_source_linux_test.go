//go:build linux

package host

import (
	"context"
	"testing"
)

func TestReservedColdInputRejectsDataOnlyAndCopiedIssuers(t *testing.T) {
	for _, incoming := range []*slotReservedStartupIncomingV2{nil, {}, {used: true}} {
		if op, err := acceptSlotReservedStartupIncomingV2(context.Background(), incoming); err == nil || op != nil {
			t.Fatal("data issued a reserved native startup operation", op, err)
		}
	}
	var input originalSlotStartupV2
	if err := input.checkClosedOperandsV2(context.Background()); err == nil {
		t.Fatal("data issued original reserved operands")
	}
	original := &originalSlotStartupV2{}
	original.issuer = original
	copied := &originalSlotStartupV2{issuer: original}
	if err := copied.checkClosedOperandsV2(context.Background()); err == nil {
		t.Fatal("copied input issued native reservation custody")
	}
	var artifacts slotColdArtifactsV2
	if err := artifacts.checkDiskV2(context.Background(), nil, false); err == nil {
		t.Fatal("data issued a reserved disk delegate")
	}
	if err := artifacts.closeV2(nil); err == nil {
		t.Fatal("data closed another artifact source")
	}
	var takeover slotColdTakeoverV2
	if err := takeover.checkV2(context.Background()); err == nil {
		t.Fatal("data issued an actual new native Source handoff")
	}
}
