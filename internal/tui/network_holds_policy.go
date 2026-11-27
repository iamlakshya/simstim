package tui

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type policyHoldReader interface {
	HasFeature(string) bool
	PendingNetworkRequests(context.Context, string) (api.NetworkHoldSnapshot, error)
}

func readPolicyHolds(ctx context.Context, connection policyConn, id string) (*api.NetworkHoldSnapshot, error) {
	features, ok := connection.(interface{ HasFeature(string) bool })
	if !ok || !features.HasFeature(api.FeatureNetworkHolds) {
		return nil, nil
	}
	c, ok := connection.(policyHoldReader)
	if !ok {
		return nil, heldPolicyUnavailable()
	}
	snapshot, err := c.PendingNetworkRequests(ctx, id)
	if err != nil {
		return nil, err
	}
	if !api.ValidNetworkHoldSnapshot(snapshot) {
		return nil, heldPolicyUnavailable()
	}
	return &snapshot, nil
}

func (d *policySheet) holdCount() int {
	if !d.holds.loaded || d.holds.snapshot.Stopped {
		return 0
	}
	return len(d.holds.snapshot.Requests)
}

func (d *policySheet) policyRowCount() int { return d.holdCount() + len(d.policy.Rules) }

func (d *policySheet) selectedRuleIndex() int { return d.sel - d.holdCount() }

func (d *policySheet) selectedHold() (api.PendingNetworkRequest, bool) {
	if d.log || d.sel < 0 || d.sel >= d.holdCount() {
		return api.PendingNetworkRequest{}, false
	}
	return d.holds.snapshot.Requests[d.sel], true
}

func (d *policySheet) applyPolicyRead(policy api.Policy, pending *api.NetworkHoldSnapshot) {
	firstRead := !d.holds.loaded
	previous, wasHeld := d.selectedHold()
	ruleIndex := d.selectedRuleIndex()
	if wasHeld {
		d.holds.selected = previous.ID
	}
	if pending == nil {
		d.holds = networkHoldView{}
	} else if !d.holds.apply(*pending) {
		d.holds.dirty, d.err = true, heldPolicyUnavailable()
		return
	}
	d.policy = policy
	if wasHeld {
		d.sel = slices.IndexFunc(d.holds.snapshot.Requests, func(request api.PendingNetworkRequest) bool { return request.ID == d.holds.selected })
		if d.sel < 0 {
			d.sel = 0
		}
	} else if d.holdCount() > 0 && firstRead {
		d.sel = 0
	} else {
		d.sel = d.holdCount() + max(ruleIndex, 0)
	}
	d.sel = min(d.sel, max(d.policyRowCount()-1, 0))
}

func heldPolicyUnavailable() error {
	return &client.UserError{Code: "policy_pending", Kind: client.KindFailed,
		Msg: "live held network requests are unavailable", Hint: "Refresh the policy, or reopen it after updating the host helper."}
}

func (m *Model) refusePolicyHold(d *policySheet) tea.Cmd {
	request, ok := d.selectedHold()
	if !ok {
		return nil
	}
	return m.changePolicyHold(d, func(ctx context.Context, connection policyConn) error {
		c, ok := connection.(interface {
			RefuseNetworkRequest(context.Context, string, string) error
		})
		if !ok {
			return heldPolicyUnavailable()
		}
		return c.RefuseNetworkRequest(ctx, d.id, request.ID)
	}, "Refused the held network request.")
}

// Each action retains this exact sheet operation and accepted connection.
// Display fields never enter the typed opaque-ID request.
func (m *Model) changePolicyHold(d *policySheet, run func(context.Context, policyConn) error, notice string) tea.Cmd {
	if m.modal != d || d.closed || d.loading || m.transferInputBlocked() {
		return nil
	}
	if d.holds.dirty {
		d.err = heldPolicyUnavailable()
		return nil
	}
	c, err := m.policyConnection(d)
	if err != nil {
		d.err = err
		return nil
	}
	ctx, seq := d.begin(m)
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return policyMsg{owner: d, seq: seq, err: err}
		}
		return policyMsg{owner: d, seq: seq, notice: notice, err: run(ctx, c)}
	})
}
