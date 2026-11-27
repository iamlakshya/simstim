package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func listedHoldFixture() api.NetworkHoldSnapshot {
	opened := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	first := api.PendingNetworkRequest{Version: 1, ID: strings.Repeat("a", 32), Revision: 1, Host: "private target", Redacted: true, Port: 443, Opened: opened, Expires: opened.Add(5 * time.Minute), Status: api.NetworkHoldOpen}
	second := first
	second.ID, second.Waiting = strings.Repeat("b", 32), 2
	return api.NetworkHoldSnapshot{Version: 1, Revision: 1, Requests: []api.PendingNetworkRequest{first, second}}
}

func TestPolicyListReadsHeldRequestsOnItsOriginalSessionConnection(t *testing.T) {
	newTestHome(t)
	host, transport := fakeBox(t, []client.Session{{ID: "hold2345", Name: "work", Alive: true, State: api.StateIdle}})
	transport.HostID = strings.Repeat("a", 32)
	if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, transport.HostID, ""); err != nil {
		t.Fatal(err)
	}
	transport.Features = []string{"policy", api.FeatureNetworkHolds}
	snapshot := listedHoldFixture()
	transport.Answers = map[string]any{"policy.ls": api.Policy{Rules: []api.Rule{{ID: "normal-rule", Door: api.DoorNet, Target: "allowed.example", Layer: api.LayerSession, Allow: true}}}, api.OpPolicyPending: snapshot}
	out := ok(t, "policy", "ls", "--in", host.Name+"/hold2345")
	first, second, rule := strings.Index(out, snapshot.Requests[0].ID), strings.Index(out, snapshot.Requests[1].ID), strings.Index(out, "normal-rule")
	if first < 0 || second <= first || rule <= second || !strings.Contains(out, "0 waiting") || !strings.Contains(out, "2 waiting") {
		t.Fatalf("held IDs merged, vanished after timeout or followed ordinary rules: %q", out)
	}
	if ops := transport.Ops(); slices.Contains(ops, "policy.allow") || slices.Contains(ops, api.OpPolicyAnswer) || slices.Contains(ops, api.OpPolicyResolve) {
		t.Fatalf("listing used display text as a positive operation: %q", ops)
	}
	out = ok(t, "policy", "ls", "--in", host.Name+"/hold2345", "--json")
	var result struct {
		Pending api.NetworkHoldSnapshot `json:"pending_network"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil || len(result.Pending.Requests) != 2 || result.Pending.Requests[0].ID != snapshot.Requests[0].ID || !result.Pending.Requests[0].Expires.Equal(snapshot.Requests[0].Expires) {
		t.Fatalf("machine output lost original token/deadline: %+v %v", result, err)
	}
}

func TestPolicyListOlderHelperDoesNotInventAnEmptyHeldSnapshot(t *testing.T) {
	newTestHome(t)
	host, transport := fakeBox(t, []client.Session{{ID: "hold2345", Name: "work", Alive: true, State: api.StateIdle}})
	transport.Features = []string{"policy"}
	transport.Answers = map[string]any{"policy.ls": api.Policy{}}
	out := ok(t, "policy", "ls", "--in", host.Name+"/hold2345", "--json")
	if strings.Contains(out, "pending_network") || slices.Contains(transport.Ops(), api.OpPolicyPending) {
		t.Fatalf("unsupported producer gained a zero list or query: %q %q", out, transport.Ops())
	}
}

func TestPolicyListInvalidHeldSnapshotWritesNoPartialPolicy(t *testing.T) {
	snapshot := listedHoldFixture()
	snapshot.Requests[1].ID = snapshot.Requests[0].ID
	for _, asJSON := range []bool{false, true} {
		var err error
		out, _ := captureOutput(t, func() {
			err = printListedPolicy(client.Host{Name: "box"}, client.Session{ID: "hold2345"}, api.Policy{}, &snapshot, asJSON, time.Now())
		})
		if err == nil || out != "" {
			t.Fatalf("invalid complete snapshot leaked partial output: %q %v", out, err)
		}
	}
}
