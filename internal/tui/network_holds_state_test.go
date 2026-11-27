package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func networkHoldViewFixture() api.NetworkHoldSnapshot {
	opened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID = strings.Repeat("b", 32)
	return api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: []api.PendingNetworkRequest{first, second}}
}

func TestPendingNetworkViewKeepsSelectionByOpaqueIDAcrossMaskedReorder(t *testing.T) {
	var view networkHoldView
	first := networkHoldViewFixture()
	if !view.apply(first) {
		t.Fatal("complete snapshot refused")
	}
	view.selected = first.Requests[1].ID
	second := first
	second.Revision = 2
	second.Requests = []api.PendingNetworkRequest{first.Requests[1], first.Requests[0]}
	if !view.apply(second) {
		t.Fatal("new complete revision refused")
	}
	current, ok := view.current()
	if !ok || current.ID != first.Requests[1].ID || len(view.snapshot.Requests) != 2 {
		t.Fatal("masked host/port or index replaced the opaque selection")
	}
	second.Requests[0].ID = strings.Repeat("c", 32)
	if current, _ := view.current(); current.ID != first.Requests[1].ID {
		t.Fatal("caller mutation changed retained display state")
	}
}

func TestPendingNetworkInvalidationRequiresACompleteNewSnapshot(t *testing.T) {
	var view networkHoldView
	first := networkHoldViewFixture()
	if !view.apply(first) || !view.invalidate(3) || !view.dirty {
		t.Fatal("new revision did not request a full refresh")
	}
	if current, ok := view.current(); !ok || current.ID != first.Requests[0].ID || len(view.snapshot.Requests) != 2 {
		t.Fatal("a revision-only event invented or removed questions")
	}
	intermediate := first
	intermediate.Revision = 2
	if view.apply(intermediate) || !view.dirty || view.snapshot.Revision != 1 {
		t.Fatal("a response older than the observed invalidation cleared the refresh")
	}
	newer := first
	newer.Revision = 3
	if !view.apply(newer) || view.dirty || view.invalidate(2) {
		t.Fatal("complete revision did not settle stale invalidations")
	}
	if view.apply(first) || view.snapshot.Revision != 3 {
		t.Fatal("an old snapshot replaced current display state")
	}
}

func TestPendingNetworkViewStopsSelectionWithoutInventingReplacement(t *testing.T) {
	var view networkHoldView
	snapshot := networkHoldViewFixture()
	if !view.apply(snapshot) {
		t.Fatal("fixture refused")
	}
	snapshot.Revision, snapshot.Stopped = 2, true
	if !view.apply(snapshot) || view.selected != "" {
		t.Fatal("stopped lifetime retained a selected question")
	}
	if _, ok := view.current(); ok {
		t.Fatal("stopped display state supplied a selected descriptor")
	}
	revived := networkHoldViewFixture()
	revived.Revision = 3
	if view.apply(revived) {
		t.Fatal("a retired view revived a new actor without an owner change")
	}
	invalid := networkHoldViewFixture()
	invalid.Revision = 3
	invalid.Requests[1].ID = invalid.Requests[0].ID
	if view.apply(invalid) || !view.snapshot.Stopped {
		t.Fatal("a partial/invalid reply revived stopped questions")
	}
}
