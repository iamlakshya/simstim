package slotlaunch

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// CapacityProjectionMaxV1 bounds one derived advisory slot observation.
const CapacityProjectionMaxV1 = 4096

// CapacityCountsV1 partitions the fixed kernel slot inventory. Occupied means
// a held lease, not a running VM. These observations reserve or authorise nothing.
type CapacityCountsV1 struct {
	Total       int `json:"total"`
	Occupied    int `json:"occupied"`
	Free        int `json:"free"`
	Quarantined int `json:"quarantined"`
}

// CapacityProjectionV1 is derived root metadata. Missing Counts is unknown.
// No field can issue a source, session, account or native execution permission.
type CapacityProjectionV1 struct {
	UID          uint32            `json:"uid"`
	BootID       string            `json:"boot_id"`
	Generation   string            `json:"generation"`
	ObservedUnix int64             `json:"observed_unix"`
	Counts       *CapacityCountsV1 `json:"counts,omitempty"`
}

// CapacityProjectionRelativeV1 names only the fixed caller-scoped metadata.
// Native producers and observers obtain uid from their actual local caller.
func CapacityProjectionRelativeV1(uid uint32) string {
	return "root-steps/machine/slots/capacity/" + strconv.FormatUint(uint64(uid), 10) + ".json"
}

var capacityGenerationV1 = regexp.MustCompile(`^[0-9a-f]{32}$`)
var capacityBootV1 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func completeCapacityFields(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		raw, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return false
		}
	}
	return true
}

func parseCapacityProjectionV1(raw []byte, uid uint32, boot string, now time.Time) (CapacityProjectionV1, bool) {
	var record CapacityProjectionV1
	if len(raw) == 0 || len(raw) > CapacityProjectionMaxV1 || !api.ExactJSONFields(raw, record) {
		return record, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !completeCapacityFields(fields, "uid", "boot_id", "generation", "observed_unix") || json.Unmarshal(raw, &record) != nil {
		return CapacityProjectionV1{}, false
	}
	observed := time.Unix(record.ObservedUnix, 0)
	if uid == 0 || record.UID != uid || !capacityBootV1.MatchString(boot) || record.BootID != boot || !capacityGenerationV1.MatchString(record.Generation) || record.ObservedUnix <= 0 || now.IsZero() || observed.Before(now.Add(-30*time.Second)) || observed.After(now.Add(5*time.Second)) || record.Counts == nil {
		return CapacityProjectionV1{}, false
	}
	var counts map[string]json.RawMessage
	if json.Unmarshal(fields["counts"], &counts) != nil || !completeCapacityFields(counts, "total", "occupied", "free", "quarantined") || !validCapacityCountsV1(*record.Counts) {
		return CapacityProjectionV1{}, false
	}
	return record, true
}

func validCapacityCountsV1(counts CapacityCountsV1) bool {
	return counts.Total == SlotCount && counts.Occupied >= 0 && counts.Occupied <= SlotCount &&
		counts.Free >= 0 && counts.Free <= SlotCount && counts.Quarantined >= 0 && counts.Quarantined <= SlotCount &&
		counts.Occupied+counts.Free+counts.Quarantined == SlotCount
}
