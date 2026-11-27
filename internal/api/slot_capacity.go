package api

import (
	"errors"
	"strings"
	"time"
)

// SlotCapacityV1 is root-derived advisory slot-lease metadata. Occupied is
// held native leases, never a claim of running VMs, selected own-account mode,
// readiness, a reservation or permission. Actual allocation freshly enforces
// the fixed pool. The enclosing client receipt controls local freshness.
type SlotCapacityV1 struct {
	Version     int       `json:"v"`
	Known       bool      `json:"known"`
	Total       int       `json:"total"`
	Occupied    int       `json:"occupied"`
	Free        int       `json:"free"`
	Quarantined int       `json:"quarantined"`
	At          time.Time `json:"at"`
	Generation  string    `json:"generation"`
}

// ValidateSlotCapacityV1 validates current DATA only. A future version must
// be classified by the strict enclosing decoder; no data becomes authority.
func ValidateSlotCapacityV1(value *SlotCapacityV1) error {
	if value == nil {
		return nil
	}
	invalid := func() error { return errors.New("slot capacity observation is unsupported or inconsistent") }
	if value.Version != 1 {
		return invalid()
	}
	if !value.Known {
		if value.Total != 0 || value.Occupied != 0 || value.Free != 0 || value.Quarantined != 0 || !value.At.IsZero() || value.Generation != "" {
			return invalid()
		}
		return nil
	}
	if value.Total != 16 || value.Occupied < 0 || value.Free < 0 || value.Quarantined < 0 || value.Occupied > 16 || value.Free > 16 || value.Quarantined > 16 || value.Occupied+value.Free+value.Quarantined != 16 || value.At.IsZero() || len(value.Generation) != 32 || strings.Trim(value.Generation, "0123456789abcdef") != "" {
		return invalid()
	}
	return nil
}
