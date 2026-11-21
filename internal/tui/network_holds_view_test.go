package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPendingNetworkSummaryKeepsZeroWaitersAndOriginalExpiryVisible(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	request := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: now, Expires: now.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	original := request
	live := pendingNetworkSummary(request, now.Add(4*time.Minute))
	if !strings.Contains(live, "waiting for a retry") || !strings.Contains(live, "private") || !strings.Contains(live, "open until 03:09:05 UTC") {
		t.Fatalf("zero waiters hid the retained question: %q", live)
	}
	if past := pendingNetworkSummary(request, request.Expires.Add(time.Second)); !strings.Contains(past, "expired") || strings.Contains(past, "open until") {
		t.Fatalf("a stale descriptor gained a renewed deadline: %q", past)
	}
	if !reflect.DeepEqual(request, original) {
		t.Fatal("drawing changed the original opaque ID or lifetime")
	}
	request.Status = api.NetworkHoldClosed
	if got := pendingNetworkSummary(request, now); !strings.Contains(got, "closed") || strings.Contains(got, "retry") {
		t.Fatalf("a closed notification looked actionable: %q", got)
	}
}

func TestPendingNetworkSnapshotKeepsMaskedRowsDistinctAndStoppedStateClosed(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: now, Expires: now.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID, second.Waiting = strings.Repeat("b", 32), 2
	snapshot := api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: []api.PendingNetworkRequest{first, second}}
	if lines := pendingNetworkSummaries(snapshot, now); len(lines) != 2 || !strings.Contains(lines[0], "retry") || !strings.Contains(lines[1], "2 flows waiting") {
		t.Fatalf("masked identity or zero waiters lost a held record: %q", lines)
	}
	snapshot.Stopped = true
	if lines := pendingNetworkSummaries(snapshot, now); len(lines) != 1 || lines[0] != "network requests unavailable" {
		t.Fatalf("a stopped lifetime displayed actionable-looking requests: %q", lines)
	}
	snapshot.Stopped, snapshot.Version = false, 2
	if lines := pendingNetworkSummaries(snapshot, now); len(lines) != 1 || lines[0] != "network requests unavailable" {
		t.Fatalf("a future snapshot became current pending state: %q", lines)
	}
}
