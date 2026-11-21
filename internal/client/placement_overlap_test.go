package client

import (
	"github.com/iamlakshya/simstim/internal/api"
	"testing"
	"time"
)

func overlapFactsFixtureV1(budget, reserved int64, charges []api.MemoryChargeV1) api.HostFacts {
	f := hostFactsFixtureValue()
	f.MemTotal, f.MemForSessions, f.MemReserved, f.Room = budget, budget, reserved, budget-reserved
	f.MemoryKnown, f.AccountingKnown = true, true
	f.MemoryObservation = &api.MemoryObservationV1{Version: 1, Complete: true, BootID: f.BootID, At: f.At, Reserved: reserved, Charges: charges}
	return f
}

func TestPlacementResidualDoesNotDoubleChargeActualSample(t *testing.T) {
	const unit = int64(4 << 30)
	now := time.Now().UTC()
	record := placementRecordV1{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Host: "box", HostID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: "cccccccccccccccccccccccccccccccc", At: now, Memory: unit, Session: "abcd2345", Pending: true}
	f := overlapFactsFixtureV1(2*unit, unit, []api.MemoryChargeV1{{PrimaryID: record.Session, Charged: unit}})
	f.HostID = record.HostID
	for _, pending := range []bool{true, false} {
		record.Pending = pending
		residual, err := placementResidualV1([]placementRecordV1{record}, record.Host, record.HostID, record.Generation, f, nil, nil, now)
		if err != nil || residual != 0 || f.Room-residual != unit {
			t.Fatal("native charge was subtracted twice", pending, residual, err)
		}
	}
	record.Pending = true
	f.MemoryObservation.Charges[0].Charged = unit / 2
	f.MemReserved, f.MemoryObservation.Reserved, f.Room = unit/2, unit/2, 3*unit/2
	residual, err := placementResidualV1([]placementRecordV1{record}, record.Host, record.HostID, record.Generation, f, nil, nil, now)
	if err != nil || residual != unit/2 {
		t.Fatal("a smaller charge erased larger pending demand", residual, err)
	}
}

func TestPlacementResidualUnknownMetadataNeverAddsRoom(t *testing.T) {
	const unit = int64(1 << 30)
	now := time.Now().UTC()
	record := placementRecordV1{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Host: "box", HostID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: "cccccccccccccccccccccccccccccccc", At: now, Memory: unit, Session: "abcd2345", Pending: true}
	for _, kind := range []string{"absent", "incomplete", "future", "boot", "time", "total", "other primary", "duplicate", "future clock"} {
		t.Run(kind, func(t *testing.T) {
			f := overlapFactsFixtureV1(2*unit, unit, []api.MemoryChargeV1{{PrimaryID: record.Session, Charged: unit}})
			f.HostID = record.HostID
			current := record
			switch kind {
			case "absent":
				f.MemoryObservation = nil
			case "incomplete":
				f.MemoryObservation.Complete = false
			case "future":
				f.MemoryObservation.Version = 2
			case "boot":
				f.MemoryObservation.BootID = "other"
			case "time":
				f.MemoryObservation.At = f.At.Add(time.Second)
			case "total":
				f.MemoryObservation.Reserved = 0
			case "other primary":
				f.MemoryObservation.Charges[0].PrimaryID = "bcde2345"
			case "duplicate":
				f.MemoryObservation.Charges = append(f.MemoryObservation.Charges, f.MemoryObservation.Charges[0])
			case "future clock":
				current.At = now.Add(time.Hour)
				f.MemoryObservation = nil
			}
			residual, err := placementResidualV1([]placementRecordV1{current}, record.Host, record.HostID, record.Generation, f, nil, nil, now)
			if err != nil || residual != unit {
				t.Fatal("unknown metadata created room", kind, residual, err)
			}
		})
	}
}

func TestPlacementResidualRootChargeIsUsedOnlyOnce(t *testing.T) {
	const unit = int64(1 << 30)
	now := time.Now().UTC()
	r := placementRecordV1{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Host: "box", HostID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: "cccccccccccccccccccccccccccccccc", At: now, Memory: unit, Session: "abcd2345", Pending: true}
	other := r
	other.ID = "dddddddddddddddddddddddddddddddd"
	f := overlapFactsFixtureV1(3*unit, unit, []api.MemoryChargeV1{{PrimaryID: r.Session, Charged: unit}})
	f.HostID = r.HostID
	residual, err := placementResidualV1([]placementRecordV1{r, other}, r.Host, r.HostID, r.Generation, f, nil, nil, now)
	if err != nil || residual != unit {
		t.Fatal("one root covered two pending records", residual, err)
	}
	other.Session = "bcde2345"
	residual, err = placementResidualV1([]placementRecordV1{r, other}, r.Host, r.HostID, r.Generation, f, nil, nil, now)
	if err != nil || residual != unit {
		t.Fatal("another member borrowed a root charge", residual, err)
	}
}

func TestPlacementResidualTenPendingStartsUseFiveSlotsEach(t *testing.T) {
	const unit = int64(1 << 30)
	ids := []string{"abcd2345", "bcde2345", "cdef2345", "defg2345", "efgh2345", "fghi2345", "ghij2345", "hijk2345", "ijkl2345", "jklm2345"}
	now := time.Now().UTC()
	hosts := []string{"box", "other"}
	records := []placementRecordV1{}
	charges := map[string][]api.MemoryChargeV1{"box": {}, "other": {}}
	counts := map[string]int{}
	for i, id := range ids {
		chosen := ""
		best := int64(-1)
		for _, host := range hosts {
			f := overlapFactsFixtureV1(5*unit, int64(counts[host])*unit, charges[host])
			f.HostID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			residual, err := placementResidualV1(records, host, f.HostID, "cccccccccccccccccccccccccccccccc", f, nil, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			room := f.Room - residual
			if room >= unit && room > best {
				chosen, best = host, room
			}
		}
		if chosen == "" {
			t.Fatal("free native slot was rejected", i, counts)
		}
		records = append(records, placementRecordV1{ID: id + "aaaaaaaaaaaaaaaaaaaaaaaa", Host: chosen, HostID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Generation: "cccccccccccccccccccccccccccccccc", At: now, Memory: unit, Session: id, Pending: true})
		charges[chosen] = append(charges[chosen], api.MemoryChargeV1{PrimaryID: id, Charged: unit})
		counts[chosen]++
	}
	if counts["box"] != 5 || counts["other"] != 5 {
		t.Fatal(counts)
	}
}
