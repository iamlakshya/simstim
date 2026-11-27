package api

import (
	"testing"
	"time"
)

func TestMemoryObservationRejectsDifferentSamplesAndTotals(t *testing.T) {
	at := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	facts := HostFacts{BootID: "boot", At: at, MemoryKnown: true, AccountingKnown: true, MemForSessions: 8 << 30, MemReserved: 4 << 30, Room: 4 << 30}
	valid := &MemoryObservationV1{Version: 1, Complete: true, BootID: facts.BootID, At: at, Reserved: facts.MemReserved, Charges: []MemoryChargeV1{{PrimaryID: "abcd2345", Charged: 4 << 30}}}
	if err := ValidateMemoryObservationV1(valid, &facts); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"version", "incomplete", "boot", "time", "reserved", "sum", "member", "negative", "duplicate", "nil array"} {
		t.Run(kind, func(t *testing.T) {
			value := CloneMemoryObservationV1(valid)
			switch kind {
			case "version":
				value.Version++
			case "incomplete":
				value.Complete = false
			case "boot":
				value.BootID = "different"
			case "time":
				value.At = value.At.Add(time.Nanosecond)
			case "reserved":
				value.Reserved--
			case "sum":
				value.Charges[0].Charged--
			case "member":
				value.Charges[0].PrimaryID = "../foreign"
			case "negative":
				value.Charges[0].Charged = -1
			case "duplicate":
				value.Charges = append(value.Charges, MemoryChargeV1{PrimaryID: "abcd2345"})
			case "nil array":
				value.Charges = nil
			}
			if err := ValidateMemoryObservationV1(value, &facts); err == nil {
				t.Fatal("partial or different accounting sample became extra room")
			}
		})
	}
	copy := CloneMemoryObservationV1(valid)
	copy.Charges[0].Charged = 1
	if valid.Charges[0].Charged != 4<<30 {
		t.Fatal("public metadata copy mutated original native sample")
	}
}

func TestEmptyMemoryObservationRequiresActualKnownZeroSample(t *testing.T) {
	at := time.Now().UTC()
	facts := HostFacts{BootID: "boot", At: at, MemoryKnown: true, AccountingKnown: true, MemForSessions: 8, Room: 8}
	value := &MemoryObservationV1{Version: 1, Complete: true, BootID: facts.BootID, At: at, Charges: []MemoryChargeV1{}}
	if err := ValidateMemoryObservationV1(value, &facts); err != nil {
		t.Fatal(err)
	}
	facts.AccountingKnown = false
	if err := ValidateMemoryObservationV1(value, &facts); err == nil {
		t.Fatal("unknown accounting became a complete empty native sample")
	}
}
