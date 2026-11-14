package files

import "testing"

func TestFreeSpaceBoundaryHasNoRoundingOrOverflow(t *testing.T) {
	const maximum = ^uint64(0)
	for _, test := range []struct {
		space Space
		low   bool
	}{
		{Space{Available: 5, Total: 100}, false},
		{Space{Available: 4, Total: 100}, true},
		{Space{Available: 5, Total: 101}, true},
		{Space{Available: maximum / 20, Total: maximum}, true},
		{Space{Available: maximum/20 + 1, Total: maximum}, false},
	} {
		if got := test.space.BelowFivePercent(); got != test.low {
			t.Fatalf("%+v: low = %v", test.space, got)
		}
	}
}
