package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Optional overlap metadata must never make an otherwise valid facts response
// too large. This runs after all existing facts fields, without another scan.
func trimHostMemoryObservationFrameV1(facts *api.HostFacts) {
	if facts == nil || facts.MemoryObservation == nil {
		return
	}
	raw, err := json.Marshal(facts)
	if err != nil || len(raw) > proto.MaxFrame-4096 {
		facts.MemoryObservation = nil
	}
}
