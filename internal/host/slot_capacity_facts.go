package host

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
)

// appendSlotCapacityFactsV1 runs only after HostFacts' original normal helper
// guard, outside placement locks and before its final identity check. The
// helper reads bounded root-owned current-UID/current-boot derived metadata;
// it never runs sudo, a subprocess, a probe, an allocator or a native start.
func appendSlotCapacityFactsV1(ctx context.Context, facts *api.HostFacts) {
	if ctx == nil || facts == nil || ctx.Err() != nil {
		return
	}
	projection, known := slotlaunch.CapacityNativeV1()
	if !known || projection.Counts == nil || projection.BootID != facts.BootID || ctx.Err() != nil {
		return
	}
	counts := projection.Counts
	value := &api.SlotCapacityV1{Version: 1, Known: true, Total: counts.Total, Occupied: counts.Occupied, Free: counts.Free, Quarantined: counts.Quarantined, At: time.Unix(projection.ObservedUnix, 0).UTC(), Generation: projection.Generation}
	if api.ValidateSlotCapacityV1(value) != nil {
		return
	}
	facts.SlotCapacity = value
}
