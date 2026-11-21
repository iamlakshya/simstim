package host

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostMemoryObservationCannotChangeKnownRoomOrOverflowFrame(t *testing.T) {
	at := time.Now().UTC()
	observed := memoryRoomFactsV1{Known: true, BootID: "boot", At: at, Budget: 8, Reserved: 4, Room: 4}
	beginMemoryObservationV1(&observed)
	observeMemoryChargeV1(&observed, "abcd2345", 4)
	finishMemoryObservationV1(&observed)
	facts := api.HostFacts{BootID: "boot", At: at, MemoryKnown: true, MemForSessions: 8}
	applyHostMemoryFactsV1(&facts, observed, nil)
	if !facts.AccountingKnown || facts.Room != 4 || facts.MemoryObservation == nil {
		t.Fatal("same accounting sample did not project optional overlap metadata", facts)
	}
	facts.MemoryObservation.Charges[0].Charged = 1
	if observed.Observation.Charges[0].Charged != 4 {
		t.Fatal("public projection mutated original protected sample")
	}
	facts.Walls = []api.WallProbe{{Reason: strings.Repeat("x", proto.MaxFrame)}}
	trimHostMemoryObservationFrameV1(&facts)
	if facts.MemoryObservation != nil || facts.Room != 4 || facts.MemReserved != 4 || !facts.AccountingKnown {
		t.Fatal("optional frame pressure changed native capacity or kept oversized metadata", facts.Room)
	}
}
