package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// This records UI dispatch only. Actual Source and physical frame gates live
// in the client/backend tests; this recorder cannot make an allowance.
type heldPolicyRecorder struct {
	*policyLifecycleConn
	snapshot api.NetworkHoldSnapshot
	features bool
	reads    []string
	refused  []string
}

func (c *heldPolicyRecorder) HasFeature(feature string) bool {
	return feature == api.FeatureNetworkHolds && c.features
}

func (c *heldPolicyRecorder) PendingNetworkRequests(ctx context.Context, id string) (api.NetworkHoldSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return api.NetworkHoldSnapshot{}, err
	}
	c.reads = append(c.reads, id)
	result := c.snapshot
	result.Requests = slices.Clone(result.Requests)
	return result, nil
}

func (c *heldPolicyRecorder) RefuseNetworkRequest(ctx context.Context, id, request string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.refused = append(c.refused, id+"/"+request)
	c.snapshot.Revision++
	c.snapshot.Requests = slices.DeleteFunc(c.snapshot.Requests, func(item api.PendingNetworkRequest) bool { return item.ID == request })
	return nil
}

func heldPolicyFixture(t *testing.T) (*harness, *hostConn, *policySheet, *heldPolicyRecorder) {
	t.Helper()
	hs, h, d, ordinary := policyLifecycleFixture(t)
	c := &heldPolicyRecorder{policyLifecycleConn: ordinary, snapshot: networkHoldViewFixture(), features: true}
	h.conn = cleanConn{c}
	hs.key("ctrl+r")
	return hs, h, d, c
}

func TestPolicySheetReadsCompleteHoldsAndRefusesOnlyTheSelectedOpaqueID(t *testing.T) {
	hs, _, d, c := heldPolicyFixture(t)
	if d.err != nil || d.loading || d.holdCount() != 2 || d.sel != 0 || len(c.reads) != 1 || c.reads[0] != d.id {
		t.Fatalf("actual policy read did not load complete same-session holds: %+v", d)
	}
	view := d.view(hs.m)
	first, rule := strings.Index(view, c.snapshot.Requests[0].ID), strings.Index(view, d.policy.Rules[0].ID)
	if first < 0 || rule <= first || !strings.Contains(view, "waiting for a retry") {
		t.Fatalf("held requests did not precede rules or zero-waiter record vanished: %q", view)
	}
	hs.key("backspace")
	if c.mutations.Load() != 0 {
		t.Fatal("the selected held row removed an ordinary rule")
	}
	hs.key("down")
	wanted := c.snapshot.Requests[1].ID
	hs.key("x")
	if len(c.refused) != 1 || c.refused[0] != d.id+"/"+wanted || c.allows.Load() != 0 || d.holdCount() != 1 || len(c.reads) != 2 {
		t.Fatalf("refusal parsed a display host, allowed it or failed to refresh: %q %+v", c.refused, d)
	}
}

func TestPolicySheetOlderHelperOmitsHoldsAndMalformedSnapshotCannotInstall(t *testing.T) {
	hs, _, d, c := heldPolicyFixture(t)
	c.features = false
	hs.key("ctrl+r")
	if d.holds.loaded || d.holdCount() != 0 || len(c.reads) != 1 {
		t.Fatal("older helper gained a synthetic complete list or extra read")
	}
	c.features = true
	c.snapshot.Requests[1].ID = c.snapshot.Requests[0].ID
	hs.key("ctrl+r")
	if d.err == nil || d.holds.loaded || !d.holds.dirty {
		t.Fatal("malformed complete list became current questions")
	}
	if cmd := hs.m.refusePolicyHold(d); cmd != nil || len(c.refused) != 0 {
		t.Fatal("an unavailable list supplied a selected opaque request")
	}
}

func TestPolicySheetConnectionChangeAndCloseCancelQueuedHoldRefusal(t *testing.T) {
	for _, change := range []string{"generation", "close"} {
		t.Run(change, func(t *testing.T) {
			hs, h, d, c := heldPolicyFixture(t)
			cmd := hs.m.refusePolicyHold(d)
			if cmd == nil || !d.loading {
				t.Fatal("refusal was not queued under the original sheet operation")
			}
			if change == "close" {
				hs.key("esc")
			} else {
				h.gen++
				hs.m.retirePolicy(d)
			}
			if raw := cmd(); raw != nil {
				_ = hs.m.onPolicy(raw.(policyMsg))
			}
			if len(c.refused) != 0 || c.allows.Load() != 0 {
				t.Fatal("retired sheet sent a late opaque-ID action")
			}
		})
	}
}

func TestPolicySheetKeepsSelectedOpaqueIDAcrossMaskedRefreshReorder(t *testing.T) {
	hs, _, d, c := heldPolicyFixture(t)
	hs.key("down")
	wanted := c.snapshot.Requests[1].ID
	c.snapshot.Requests[0], c.snapshot.Requests[1] = c.snapshot.Requests[1], c.snapshot.Requests[0]
	c.snapshot.Revision++
	hs.key("ctrl+r")
	selected, ok := d.selectedHold()
	if !ok || selected.ID != wanted || d.sel != 0 || d.holdCount() != 2 {
		t.Fatal("masked label or old row index replaced original selection")
	}
}
