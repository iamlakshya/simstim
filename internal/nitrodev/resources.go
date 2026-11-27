package nitrodev

import "fmt"

// Resources is one native pool observation. Memory counters are bytes, while
// slots and CPUs are counts. A zero reserved pool uses contiguous allocation.
type Resources struct {
	ParentCPUs, Slots, SlotsInUse, SlotsTotal int64
	MemoryTotal, MemoryFree                   int64
}

// Reservation counts protected managed guests, including starts not yet
// visible in the native slot counters. It is data, never an admission grant.
type Reservation struct{ CPUs, PendingSlots, PendingMemory, ManagedSlots int64 }

// Check bounds a new complete guest against the actual parent budget.
func (r Resources) Check(cpus, memory int64, used Reservation) error {
	if r.ParentCPUs <= 0 || r.ParentCPUs > 1<<16 || r.Slots <= 0 || r.SlotsInUse < 0 || r.SlotsInUse > r.Slots || r.SlotsTotal < r.Slots || r.MemoryTotal < 0 || r.MemoryFree < 0 || r.MemoryFree > r.MemoryTotal || used.CPUs < 0 || used.PendingSlots < 0 || used.PendingMemory < 0 || used.ManagedSlots < 0 || cpus <= 0 || memory <= 0 {
		return fmt.Errorf("the native Nitro resource observation is unavailable or inconsistent")
	}
	if cpus > r.ParentCPUs || cpus > 1024 || used.CPUs > 5*r.ParentCPUs-cpus {
		return fmt.Errorf("the sandbox exceeds the Nitro vCPU budget")
	}
	// Native slots count every enclave, including another user's. Our pending
	// records cover the gap before creation; conservative overlap can refuse.
	occupied := max(r.SlotsInUse, used.ManagedSlots)
	if used.PendingSlots >= r.Slots-occupied {
		return fmt.Errorf("the Nitro slot budget has no room")
	}
	if r.MemoryTotal > 0 && (used.PendingMemory > r.MemoryFree || memory > r.MemoryFree-used.PendingMemory) {
		return fmt.Errorf("the sandbox exceeds the free Nitro memory pool")
	}
	return nil
}
