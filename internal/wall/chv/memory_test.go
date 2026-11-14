package chv

import (
	"math"
	"testing"
)

func TestGuestMemoryIsSeparateFromUnitReserve(t *testing.T) {
	for _, test := range []struct{ guest, reserve, limit int64 }{{1024, 132, 1156}, {4096, 144, 4240}, {256, 129, 385}, {257, 130, 387}, {8192, 160, 8352}} {
		plan, err := BudgetFor(test.guest * mebibyte)
		if err != nil {
			t.Fatal(err)
		}
		if plan.Guest != test.guest*mebibyte || plan.Reserve != test.reserve*mebibyte || plan.Limit != test.limit*mebibyte {
			t.Fatalf("budget %+v, want %+v", plan, test)
		}
	}
}

func TestGuestMemoryRejectsUnnormalisedAndOverflowingSizes(t *testing.T) {
	for _, size := range []int64{0, -mebibyte, mebibyte - 1, math.MaxInt64 / mebibyte * mebibyte} {
		if _, err := BudgetFor(size); err == nil {
			t.Fatalf("unsafe guest memory %d accepted", size)
		}
	}
}
