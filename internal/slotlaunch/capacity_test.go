package slotlaunch

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func capacityFixtureV1() (CapacityProjectionV1, time.Time) {
	now := time.Unix(1771234567, 0).UTC()
	return CapacityProjectionV1{UID: 1000, BootID: "11111111-2222-3333-4444-555555555555", Generation: strings.Repeat("a", 32),
		ObservedUnix: now.Unix(), Counts: &CapacityCountsV1{Total: SlotCount, Free: SlotCount}}, now
}

func TestCapacityProjectionNeverTurnsUnknownOrForeignIntoRoom(t *testing.T) {
	record, now := capacityFixtureV1()
	good, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if result, known := parseCapacityProjectionV1(good, record.UID, record.BootID, now); !known || result.Counts == nil || result.Counts.Free != SlotCount {
		t.Fatal("complete current slot observation was lost")
	}
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"unknown counts", strings.Replace(string(good), `,"counts":{"total":16,"occupied":0,"free":16,"quarantined":0}`, "", 1)},
		{"null counts", strings.Replace(string(good), `{"total":16,"occupied":0,"free":16,"quarantined":0}`, "null", 1)},
		{"foreign uid", strings.Replace(string(good), `"uid":1000`, `"uid":1001`, 1)},
		{"other boot", strings.Replace(string(good), `"boot_id":"1111`, `"boot_id":"2111`, 1)},
		{"duplicate uid", strings.Replace(string(good), `"uid":1000`, `"uid":1000,"uid":1000`, 1)},
		{"uid alias", strings.Replace(string(good), `"uid":1000`, `"UID":1000`, 1)},
		{"null uid", strings.Replace(string(good), `"uid":1000`, `"uid":null`, 1)},
		{"root uid", strings.Replace(string(good), `"uid":1000`, `"uid":0`, 1)},
		{"missing total", strings.Replace(string(good), `"total":16,`, "", 1)},
		{"missing occupied", strings.Replace(string(good), `"occupied":0,`, "", 1)},
		{"null occupied", strings.Replace(string(good), `"occupied":0`, `"occupied":null`, 1)},
		{"bad partition", strings.Replace(string(good), `"free":16`, `"free":15`, 1)},
		{"negative count", strings.Replace(string(good), `"occupied":0`, `"occupied":-1`, 1)},
		{"count alias", strings.Replace(string(good), `"occupied":0`, `"Occupied":0`, 1)},
		{"duplicate count", strings.Replace(string(good), `"occupied":0`, `"occupied":0,"occupied":0`, 1)},
		{"new count field", strings.Replace(string(good), `"occupied":0`, `"occupied":0,"running_vms":0`, 1)},
		{"new top field", strings.Replace(string(good), `"uid":1000`, `"uid":1000,"ready":true`, 1)},
		{"generation malformed", strings.Replace(string(good), strings.Repeat("a", 32), "generation", 1)},
		{"second object", string(good) + `{}`},
		{"oversize", string(good) + strings.Repeat(" ", CapacityProjectionMaxV1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if result, known := parseCapacityProjectionV1([]byte(tc.raw), record.UID, record.BootID, now); known || result.Counts != nil {
				t.Fatal("unknown advisory observation supplied room")
			}
		})
	}
	for _, at := range []time.Time{now.Add(31 * time.Second), now.Add(-6 * time.Second), {}} {
		if result, known := parseCapacityProjectionV1(good, record.UID, record.BootID, at); known || result.Counts != nil {
			t.Fatal("stale, future or absent clock supplied room")
		}
	}
}
