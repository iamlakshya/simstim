package client

import (
	"reflect"
	"testing"
	"time"
)

func TestNoRoomCacheUpdatePreservesOriginalFactsAgeAndOtherMetadata(t *testing.T) {
	candidate := placementCandidateFixture(t, nil, nil)
	observed := candidate.observation
	entry := placementFactsV1{HostID: observed.hostID, Generation: observed.source.generation,
		ReceivedAt: observed.receivedAt, RoundTrip: observed.roundTrip, Facts: cloneHostFacts(observed.facts)}
	facts := newNoRoomDiagnosticsV1{Known: true, BootID: entry.Facts.BootID, At: time.Now().UTC(),
		Budget: entry.Facts.MemForSessions, Reserved: entry.Facts.MemForSessions - 1, Room: 1, Needed: 2, Sandboxes: 3}
	updated, coherent := updateCachedPlacementRoomV1(entry, facts)
	if !coherent || updated.Facts.Room != 1 || updated.Facts.MemReserved != facts.Reserved || updated.Facts.Sandboxes != 3 {
		t.Fatal(updated, coherent)
	}
	if !updated.ReceivedAt.Equal(entry.ReceivedAt) || !updated.Facts.At.Equal(entry.Facts.At) || updated.RoundTrip != entry.RoundTrip ||
		!reflect.DeepEqual(updated.Facts.Walls, entry.Facts.Walls) || !reflect.DeepEqual(updated.Facts.ImageDigests, entry.Facts.ImageDigests) {
		t.Fatal("room refusal renewed unrelated facts or changed their provenance")
	}
	facts.BootID = "another-boot"
	if _, coherent := updateCachedPlacementRoomV1(entry, facts); coherent {
		t.Fatal("room from another boot was merged into original facts")
	}
	facts.BootID, facts.Budget = entry.Facts.BootID, entry.Facts.MemTotal+1
	if _, coherent := updateCachedPlacementRoomV1(entry, facts); coherent {
		t.Fatal("a partial update hid a memory configuration change")
	}
}
