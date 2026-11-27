package client

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestAutoDryRunDisplaysOnlyAvailableObservationFields(t *testing.T) {
	candidate := &autoPlacementCandidate{host: Host{Name: "box"}, level: "microvm", room: 42, imageCached: true,
		need:        PlacementNeed{ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		observation: &hostFactsObservation{roundTrip: 2 * time.Millisecond, facts: api.HostFacts{}}, reason: "awake, image cached"}
	row := autoPlacementDisplayRowV1(candidate, 0)
	if row.Verdict != "chosen" || row.Image != "cached" || !row.RoomKnown || !row.RoundTripKnown || row.RoundTrip != 2*time.Millisecond {
		t.Fatal(row)
	}
	candidate.stopped, candidate.wakeClass = true, 2
	row = autoPlacementDisplayRowV1(candidate, 1)
	if row.Verdict != "fits" || row.RoomKnown || row.RoundTripKnown || row.Wake != "paid wake" {
		t.Fatal("stopped cache became live display facts", row)
	}
}
