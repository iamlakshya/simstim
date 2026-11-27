package api

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// MemoryObservationVersionV1 is the optional same-scan overlap metadata.
const MemoryObservationVersionV1 = 1

// MaxMemoryObservationChargesV1 bounds metadata, never native concurrency.
const MaxMemoryObservationChargesV1 = 32768

// MaxMemoryObservationBytesV1 leaves space in the existing four-MiB frame.
const MaxMemoryObservationBytesV1 = 3 << 20

// MemoryChargeV1 is one canonical primary's charge already included in Room.
// Auxiliary and resume charges use their original primary once. This record
// issues no capacity, namespace, runtime or operation authority.
type MemoryChargeV1 struct {
	PrimaryID string `json:"primary_id"`
	Charged   int64  `json:"charged"`
}

// MemoryObservationV1 is metadata from the SAME protected scan as MemReserved
// and Room. Absence, partial data or an unsupported version cannot add room.
// At preserves that scan time; it is not a clock or causality permission.
type MemoryObservationV1 struct {
	Version  int              `json:"v"`
	Complete bool             `json:"complete"`
	BootID   string           `json:"boot_id"`
	At       time.Time        `json:"at"`
	Reserved int64            `json:"reserved"`
	Charges  []MemoryChargeV1 `json:"charges"`
}

// ValidateMemoryObservationV1 checks a usable complete observation against
// its enclosing facts. A decoder also rejects duplicate/unknown JSON fields.
// Invalid optional metadata must be ignored conservatively, never repaired
// into extra room or used as a native reservation or boot grant.
func ValidateMemoryObservationV1(value *MemoryObservationV1, facts *HostFacts) error {
	if value == nil || facts == nil || value.Version != MemoryObservationVersionV1 || !value.Complete || !facts.AccountingKnown || !facts.MemoryKnown || value.BootID == "" || value.BootID != facts.BootID || value.At.IsZero() || !value.At.Equal(facts.At) || value.Reserved < 0 || value.Reserved != facts.MemReserved || facts.MemForSessions <= 0 || facts.Room != facts.MemForSessions-facts.MemReserved || value.Charges == nil || len(value.Charges) > MaxMemoryObservationChargesV1 {
		return fmt.Errorf("the memory overlap observation is incomplete or has another sample. Try: retain local in-flight charges and read fresh facts")
	}
	seen := make(map[string]struct{}, len(value.Charges))
	var total int64
	for _, charge := range value.Charges {
		if !ValidSessionID(charge.PrimaryID) || charge.Charged < 0 || total > math.MaxInt64-charge.Charged {
			return fmt.Errorf("the memory overlap observation has an invalid charge. Try: retain local in-flight charges")
		}
		if _, duplicate := seen[charge.PrimaryID]; duplicate {
			return fmt.Errorf("the memory overlap observation repeats a primary. Try: retain local in-flight charges")
		}
		seen[charge.PrimaryID] = struct{}{}
		total += charge.Charged
	}
	if total != value.Reserved {
		return fmt.Errorf("the memory overlap charges differ from the same native total. Try: retain local in-flight charges")
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > MaxMemoryObservationBytesV1 {
		return fmt.Errorf("the memory overlap observation exceeds its frame bound. Try: retain local in-flight charges")
	}
	return nil
}

// CloneMemoryObservationV1 returns an independent metadata copy. Mutating a
// cache or public facts projection cannot mutate the original sample.
func CloneMemoryObservationV1(value *MemoryObservationV1) *MemoryObservationV1 {
	if value == nil {
		return nil
	}
	result := *value
	if value.Charges != nil {
		result.Charges = append([]MemoryChargeV1{}, value.Charges...)
	}
	return &result
}
