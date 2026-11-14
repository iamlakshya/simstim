package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func policyOwner(d modal) *policySheet {
	d = questionOwner(d)
	p, _ := d.(*policySheet)
	return p
}

func (d *policySheet) finishOp() {
	if d.cancel != nil {
		d.cancel()
	}
	d.ctx, d.cancel = nil, nil
}

func (d *policySheet) begin(m *Model) (context.Context, uint64) {
	d.finishOp()
	d.seq++
	d.ctx, d.cancel = context.WithCancel(m.backgroundContext())
	d.loading, d.err = true, nil
	return d.ctx, d.seq
}

func (d *policySheet) stop() {
	d.seq++
	d.closed, d.loading = true, false
	d.finishOp()
}

func (d *policySheet) interrupted(err error) {
	d.seq++
	d.finishOp()
	d.loading, d.err = false, err
}

func policyInterrupted() error {
	return &client.UserError{Msg: "the policy operation stopped before its result was applied",
		Hint: "Read the current policy before making another change.", Code: "cancelled", Kind: client.KindCancelled}
}

func (m *Model) policyConnection(d *policySheet) (policyConn, error) {
	h := m.hostByName(d.host)
	if h == nil || !h.online() || h.gen != d.gen || !client.SameEntry(h.host, d.origin) {
		return nil, &client.UserError{Msg: "the policy host connection changed",
			Hint: "Reconnect the host, then open the policy again.", Code: "offline"}
	}
	c, ok := unclean(h.conn).(policyConn)
	if !ok {
		return nil, policyUnavailable()
	}
	return c, nil
}

func (m *Model) resumePolicy() tea.Cmd {
	if d := policyOwner(m.modal); d != nil && d.loading {
		d.interrupted(policyInterrupted())
	}
	return nil
}

func (m *Model) retirePolicy(modalBefore modal) {
	before, current := policyOwner(modalBefore), policyOwner(m.modal)
	if before != nil && before != current {
		before.stop()
	}
	if current != nil && !current.closed {
		if _, err := m.policyConnection(current); err != nil {
			current.interrupted(err)
		}
	}
}
