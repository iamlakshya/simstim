package api

import (
	"strings"
	"testing"
	"time"
)

func TestSlotCapacityIsStrictAdvisoryData(t *testing.T) {
	valid := SlotCapacityV1{Version: 1, Known: true, Total: 16, Occupied: 1, Free: 14, Quarantined: 1, At: time.Unix(1, 0).UTC(), Generation: strings.Repeat("a", 32)}
	if err := ValidateSlotCapacityV1(&valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*SlotCapacityV1){
		func(v *SlotCapacityV1) { v.Version = 2 }, func(v *SlotCapacityV1) { v.Total = 17 }, func(v *SlotCapacityV1) { v.Free = 15 }, func(v *SlotCapacityV1) { v.Occupied = -1 }, func(v *SlotCapacityV1) { v.At = time.Time{} }, func(v *SlotCapacityV1) { v.Generation = "TOKEN" },
	} {
		value := valid
		change(&value)
		if ValidateSlotCapacityV1(&value) == nil {
			t.Fatal("malformed slot metadata became known")
		}
	}
	unknown := SlotCapacityV1{Version: 1}
	if ValidateSlotCapacityV1(&unknown) != nil || ValidateSlotCapacityV1(nil) != nil {
		t.Fatal("unknown observation became an error or count")
	}
	unknown.Free = 16
	if ValidateSlotCapacityV1(&unknown) == nil {
		t.Fatal("unknown observation fabricated available capacity")
	}
}
