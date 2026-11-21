package host

import (
	"math"

	"github.com/iamlakshya/simstim/internal/api"
)

// The optional observation is built alongside the existing accounting scan.
// Omission never changes native admission or the number of supported starts.
const memoryObservationLimitV1 = api.MaxMemoryObservationChargesV1

func beginMemoryObservationV1(facts *memoryRoomFactsV1) {
	if facts == nil {
		return
	}
	facts.Observation = &api.MemoryObservationV1{Version: api.MemoryObservationVersionV1, BootID: facts.BootID, At: facts.At, Charges: []api.MemoryChargeV1{}}
	facts.observationIDs = make(map[string]struct{})
}

func observeMemoryChargeV1(facts *memoryRoomFactsV1, primaryID string, charge int64) {
	if facts == nil || facts.Observation == nil || charge == 0 {
		return
	}
	observation := facts.Observation
	if observation.Complete || facts.observationIDs == nil {
		facts.Observation, facts.observationIDs = nil, nil
		return
	}
	_, duplicate := facts.observationIDs[primaryID]
	if !api.ValidSessionID(primaryID) || charge < 0 || duplicate || len(observation.Charges) >= memoryObservationLimitV1 ||
		observation.Reserved > math.MaxInt64-charge {
		facts.Observation, facts.observationIDs = nil, nil
		return
	}
	facts.observationIDs[primaryID] = struct{}{}
	observation.Charges = append(observation.Charges, api.MemoryChargeV1{PrimaryID: primaryID, Charged: charge})
	observation.Reserved += charge
}

func finishMemoryObservationV1(facts *memoryRoomFactsV1) {
	if facts == nil || facts.Observation == nil {
		return
	}
	observed := facts.Observation
	if !facts.Known || observed.Version != api.MemoryObservationVersionV1 || observed.BootID == "" || observed.BootID != facts.BootID ||
		observed.At.IsZero() || !observed.At.Equal(facts.At) || observed.Reserved != facts.Reserved ||
		facts.Room != facts.Budget-facts.Reserved {
		facts.Observation, facts.observationIDs = nil, nil
		return
	}
	observed.Complete = true
	facts.observationIDs = nil
}
