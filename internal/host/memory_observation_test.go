package host

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestMemoryObservationUsesOneFinalPrimaryChargeAndSameSample(t *testing.T) {
	facts := memoryRoomFactsV1{Known: true, BootID: "boot", At: time.Now().UTC(), Budget: 8, Reserved: 5, Room: 3}
	beginMemoryObservationV1(&facts)
	observeMemoryChargeV1(&facts, "abcd2345", 4)
	observeMemoryChargeV1(&facts, "bcde2345", 1)
	finishMemoryObservationV1(&facts)
	if facts.Observation == nil || !facts.Observation.Complete || facts.Observation.Reserved != facts.Reserved ||
		len(facts.Observation.Charges) != 2 || !facts.Observation.At.Equal(facts.At) {
		t.Fatal("the exact scan sample was not retained", facts.Observation)
	}
}

func TestMemoryObservationOptionalFailureNeverChangesAdmissionFacts(t *testing.T) {
	for _, kind := range []string{"duplicate", "invalid", "negative", "too many", "different sample", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			facts := memoryRoomFactsV1{Known: true, BootID: "boot", At: time.Now().UTC(), Budget: 8, Reserved: 4, Room: 4}
			beginMemoryObservationV1(&facts)
			observeMemoryChargeV1(&facts, "abcd2345", 4)
			switch kind {
			case "duplicate":
				observeMemoryChargeV1(&facts, "abcd2345", 1)
			case "invalid":
				observeMemoryChargeV1(&facts, "not-a-member", 1)
			case "negative":
				observeMemoryChargeV1(&facts, "bcde2345", -1)
			case "too many":
				facts.Observation.Charges = make([]api.MemoryChargeV1, memoryObservationLimitV1)
				observeMemoryChargeV1(&facts, "bcde2345", 1)
			case "different sample":
				facts.Observation.Reserved = 3
			case "unknown":
				facts.Known = false
			}
			finishMemoryObservationV1(&facts)
			if facts.Observation != nil || facts.Budget != 8 || facts.Reserved != 4 || facts.Room != 4 {
				t.Fatal("optional metadata changed native accounting or pretended completeness", facts)
			}
		})
	}
}
