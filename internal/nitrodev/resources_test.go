package nitrodev

import "testing"

func TestNativeBudgetsCountPendingReservations(t *testing.T) {
	base := Resources{ParentCPUs: 8, Slots: 4, SlotsTotal: 40, MemoryTotal: 8 << 30, MemoryFree: 4 << 30}
	if err := base.Check(8, 1<<30, Reservation{CPUs: 32, PendingSlots: 2, PendingMemory: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name         string
		r            Resources
		cpus, memory int64
		used         Reservation
	}{
		{"parent CPUs", base, 9, 1 << 30, Reservation{}},
		{"total CPUs", base, 8, 1 << 30, Reservation{CPUs: 33}},
		{"pending slots", base, 1, 1 << 30, Reservation{PendingSlots: 4}},
		{"native external slots", Resources{ParentCPUs: 8, Slots: 4, SlotsTotal: 40, SlotsInUse: 4}, 1, 1 << 30, Reservation{}},
		{"pending memory", base, 1, 2 << 30, Reservation{PendingMemory: 3 << 30}},
		{"unknown CPU budget", Resources{Slots: 4, SlotsTotal: 40}, 1, 1 << 30, Reservation{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.r.Check(tc.cpus, tc.memory, tc.used) == nil {
				t.Fatal("over-budget sandbox admitted")
			}
		})
	}
	// No reserved pool is a real contiguous-allocation mode, not zero RAM.
	if err := (Resources{ParentCPUs: 8, Slots: 4, SlotsTotal: 40}).Check(1, 1<<30, Reservation{}); err != nil {
		t.Fatal(err)
	}
}
