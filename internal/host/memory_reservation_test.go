package host

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func memoryReservationFixtureV1(driver string, now time.Time) Manifest {
	level := driver
	if driver == "chv" || driver == "firecracker" {
		level = "microvm"
	}
	return Manifest{ID: "aaaa1111", Wall: &WallRecord{Driver: driver, Level: level},
		Run: Runtime{BootID: "boot"}, Limits: Limits{MemoryMax: 512 << 20, Declared: 512 << 20},
		Status: Status{State: StateIdle, Since: now}}
}

func TestEveryWalledUnitReservesDeclaredEvenWhenIdleOrSocketIsDead(t *testing.T) {
	now := time.Now().UTC()
	for _, driver := range []string{"process", "gvisor", "chv", "firecracker"} {
		m := memoryReservationFixtureV1(driver, now)
		charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20, current: 1})
		if err != nil || charge != 512<<20 {
			t.Fatalf("%s idle reservation became RSS: %d %v", driver, charge, err)
		}
		m.Status.State = StateStopped
		charge, err = reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20})
		if err != nil || charge != 512<<20 {
			t.Fatalf("%s metadata/socket state freed a live native unit: %d %v", driver, charge, err)
		}
	}
}

func TestReservationUsesExactTwoMinuteBoundaryAndCountsUncertainTimes(t *testing.T) {
	now := time.Now().UTC()
	m := memoryReservationFixtureV1("gvisor", now)
	m.Status.State = StateStarting
	for _, at := range []time.Time{now, now.Add(-2 * time.Minute), now.Add(time.Hour), {}} {
		m.Status.Since = at
		charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{})
		if err != nil || charge != 512<<20 {
			t.Fatalf("a starting reservation was lost: %s %d %v", at, charge, err)
		}
	}
	m.Status.Since = now.Add(-2*time.Minute - time.Nanosecond)
	if charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{}); err != nil || charge != 0 {
		t.Fatal("the two-minute expiry moved", charge, err)
	}
	m.SandboxOf = "bbbb2222"
	if charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20}); err != nil || charge != 0 {
		t.Fatal("a member was charged twice", charge, err)
	}
	m.SandboxOf, m.Run.BootID = "", "old-boot"
	if charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20}); err != nil || charge != 0 {
		t.Fatal("an old-boot record was charged", charge, err)
	}
}

func TestNoneKeepsActualUsageAndInconsistentWallCannotBorrowThatRule(t *testing.T) {
	now := time.Now().UTC()
	m := memoryReservationFixtureV1("process", now)
	m.Wall = nil
	charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, current: 1 << 20})
	if err != nil || charge != 1<<20 {
		t.Fatal("none became a declared-cap reservation", charge, err)
	}
	m.Wall = &WallRecord{Level: "none", Driver: "gvisor"}
	if charge, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20, current: 1}); err != nil || charge != 512<<20 {
		t.Fatal("an inconsistent wall understated its native reservation", charge, err)
	}
	m.Limits.Declared = 256 << 20
	if _, err := reservationChargeV1(&m, "boot", now, memoryUnitFactsV1{exists: true, maximum: 512 << 20}); err == nil {
		t.Fatal("a declared value understated the actual unit cap")
	}
}

func TestWalledDefaultsAndExplicitSizesNeverUseMachineMinimumOrClamp(t *testing.T) {
	agent := agents.Agent{Memory: agents.GB}
	limits, err := newWalledMemoryV1(NewArgs{}, "process", api.SandboxValues{}, agent)
	if err != nil || limits.MemoryMax != 4<<30 || limits.Declared != limits.MemoryMax {
		t.Fatal("machine minimum or host budget supplied a sandbox default", limits, err)
	}
	limits, err = newWalledMemoryV1(NewArgs{MemoryMax: 3 << 30}, "gvisor", api.SandboxValues{}, agent)
	if err != nil || limits.MemoryMax != 3<<30 || limits.Declared != 3<<30 {
		t.Fatal("an explicit size was clamped before admission", limits, err)
	}
	current := memoryReservationFixtureV1("process", time.Now().UTC())
	memory := "4G"
	current.Policy = &api.Policy{LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Memory: &memory}}}
	limits, err = restartLimitsForRequestV1(&current, &current, 0, true)
	if err != nil || limits.MemoryMax != 512<<20 {
		t.Fatal("restore zero replaced its protected cap with a new default", limits, err)
	}
	limits, err = restartLimitsForRequestV1(&current, &current, api.MemoryAuto, false)
	if err != nil || limits.MemoryMax != 4<<30 {
		t.Fatal("manual auto lost the actual size resolver", limits, err)
	}
}
