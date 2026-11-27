package tomlinspect

import (
	"errors"
	"testing"
)

func TestPackedChargeAccountsActualDirectoryAndSplitBoundaries(t *testing.T) {
	for _, test := range []struct{ width, count, want uint64 }{
		{192, 0, 0}, {192, 21, 3}, {192, 22, 4}, {96, 42, 3}, {96, 43, 4}, {64, 63, 3}, {64, 64, 4},
		{192, 21 * 1008, 1010}, {192, 21*1008 + 1, 1012},
	} {
		got, err := packedRecordPages(test.width, test.count)
		if err != nil || got != test.want {
			t.Fatal("physical directory boundary omitted pages", test, got, err)
		}
	}
	for _, test := range []struct{ count, want uint64 }{{0, 0}, {1, 1}, {63, 1}, {64, 3}, {127, 4}, {128, 4}, {63 * 254, 257}} {
		got, err := numericTreePages(test.count)
		if err != nil || got != test.want {
			t.Fatal("physical tree occupancy bound changed", test, got, err)
		}
	}
	if partialTreePages != 10 {
		t.Fatal("partial split allowance no longer matches actual fixed tree path")
	}
}

func TestGridChargeUsesBitsAndPackedLiveFrontier(t *testing.T) {
	// Arithmetic oracle only: these are the observed pinned-rule facts, not
	// an assertion that this test compiled or validated the rule program.
	grid, err := grammarPhysicalCounts(348, 167, 5, 4<<20)
	if err != nil || grid.bits != 233856 || grid.frontier != 3361 || grid.seen != 233856 {
		t.Fatal("exact grid arithmetic changed", grid, err)
	}
	charge, err := entryPhysicalCharge(entryRecordCounts{gridBits: grid.bits, frontierIDs: grid.frontier})
	if err != nil || charge.grammar != 19 {
		t.Fatal("bitmap/frontier acquired per-state rows or lost directory pages", charge, err)
	}
	for _, test := range []struct{ bits, want uint64 }{{0, 0}, {1, 3}, {4064 * 8, 3}, {4064*8 + 1, 4}} {
		got, err := bitmapPages(test.bits)
		if err != nil || got != test.want {
			t.Fatal("bit-grid boundary changed", test, got, err)
		}
	}
	generic, err := entryPhysicalCharge(entryRecordCounts{})
	if err != nil || generic.runs != 0 || generic.grammar != 0 {
		t.Fatal("generic shape acquired a grammar/run phase")
	}
	wide, err := grammarPhysicalCounts(2004, 2002, 0, 4<<20)
	if err != nil || wide.bits <= 4<<20 || wide.frontier != 1 {
		t.Fatal("work bound became a new width limit or full seen frontier", wide, err)
	}
}

func TestChargeArithmeticRefusesOverflowRatherThanWrappingQuota(t *testing.T) {
	if _, err := chargeAdd(^uint64(0), 1); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("page quota addition wrapped", err)
	}
	if _, err := chargeMul(^uint64(0), 2); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("page quota multiplication wrapped", err)
	}
	if _, err := grammarPhysicalCounts(1, ^uint64(0), 0, 4<<20); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("grammar width stride wrapped", err)
	}
	if _, err := packedRecordPages(192, ^uint64(0)); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("record directory addressing overflowed", err)
	}
	if got, err := chargeCeil(^uint64(0), 8); err != nil || got != uint64(1)<<61 {
		t.Fatal("ceil division used an overflowing numerator", got, err)
	}
}
