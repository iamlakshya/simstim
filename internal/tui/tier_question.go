package tui

import (
	"context"
	"errors"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type tierAnswer struct {
	tier client.Tier
	err  error
}

type tierQuestionRequest struct {
	ctx    context.Context
	host   client.Host
	answer chan tierAnswer
	ended  chan struct{}
}

type tierQuestionEndedMsg struct{ answer chan tierAnswer }

type hostTierResolvedMsg struct {
	host    client.Host
	gen     int
	tier    client.Tier
	pending bool
	err     error
	next    func() tea.Cmd
}

func (m *Model) askHostTier(ctx context.Context, h client.Host) (client.Tier, error) {
	r := tierQuestionRequest{ctx: ctx, host: h, answer: make(chan tierAnswer, 1), ended: make(chan struct{})}
	defer close(r.ended)
	answer := m.waitTier(func() tea.Msg { return m.receiveTierAnswer(ctx, r) }).(tierAnswer)
	return answer.tier, answer.err
}

func (m *Model) receiveTierAnswer(ctx context.Context, r tierQuestionRequest) tierAnswer {
	select {
	case m.tierRequests <- r:
	case <-ctx.Done():
		return tierAnswer{err: ctx.Err()}
	case <-m.ctx.Done():
		return tierAnswer{err: context.Canceled}
	}
	select {
	case answer := <-r.answer:
		return answer
	case <-ctx.Done():
		return tierAnswer{err: ctx.Err()}
	case <-m.ctx.Done():
		return tierAnswer{err: context.Canceled}
	}
}

func (m *Model) waitTier(wait func() tea.Msg) tea.Msg {
	if m.tierWait != nil {
		return m.tierWait(wait)
	}
	return wait()
}

func (m *Model) waitForTierQuestion() tea.Cmd {
	requests := m.tierRequests
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		return m.waitTier(func() tea.Msg {
			select {
			case r := <-requests:
				return r
			case <-ctx.Done():
				return nil
			}
		})
	})
}

func (m *Model) onTierQuestion(r tierQuestionRequest) tea.Cmd {
	if r.ctx.Err() != nil {
		return m.waitForTierQuestion()
	}
	m.modal = &hostTierQuestion{request: r, previous: m.modal, pick: -1}
	m.quitArmed = false
	return m.bgCmd(func(ctx context.Context) tea.Msg {
		return m.waitTier(func() tea.Msg {
			select {
			case <-r.ended:
				return tierQuestionEndedMsg{answer: r.answer}
			case <-ctx.Done():
				return nil
			}
		})
	})
}

// retireTierQuestion removes a cancelled sheet before the next program run.
// Its interrupted operation keeps the host's pending tier unchanged.
func (m *Model) retireTierQuestion() {
	for {
		q, ok := m.modal.(*hostTierQuestion)
		if !ok || q.request.ctx == nil || q.request.ctx.Err() == nil {
			return
		}
		m.modal = q.previous
	}
}

func (m *Model) onTierQuestionEnded(msg tierQuestionEndedMsg) tea.Cmd {
	if q, ok := m.modal.(*hostTierQuestion); ok && q.request.answer == msg.answer {
		m.modal = q.previous
		return m.waitForTierQuestion()
	}
	return nil
}

// withHostTier resolves current saved policy before any foreground work.
// The continuation runs on Update's goroutine after persistence succeeds.
func (m *Model) withHostTier(h *hostConn, next func() tea.Cmd) tea.Cmd {
	return m.withHostTierContext(client.WithoutWake(m.backgroundContext()), h, next)
}

func (m *Model) withHostTierContext(ctx context.Context, h *hostConn, next func() tea.Cmd) tea.Cmd {
	be, host, gen := m.be, h.host, h.gen
	return m.bgCmdFrom(ctx, func(ctx context.Context) tea.Msg {
		msg := hostTierResolvedMsg{host: host, gen: gen, next: next}
		msg.tier, msg.err = be.ResolveHostTier(ctx, host)
		if msg.err != nil {
			return msg
		}
		hosts, err := be.LoadHosts(ctx)
		if err != nil {
			msg.err = err
			return msg
		}
		if !slices.ContainsFunc(hosts, func(now client.Host) bool { return client.SameEntry(now, host) }) {
			msg.err = &client.UserError{Msg: "the saved host changed while its tier was checked", Hint: "Choose the host again.", Code: "host_changed"}
			return msg
		}
		tiers, err := be.LoadTiers(ctx, hosts)
		msg.err, msg.pending = err, tiers.NeedsAnswer(host.Name)
		return msg
	})
}

func (m *Model) onHostTierResolved(msg hostTierResolvedMsg) tea.Cmd {
	h := m.hostByName(msg.host.Name)
	if h == nil || h.gen != msg.gen || !client.SameEntry(h.host, msg.host) {
		return nil
	}
	if msg.err != nil {
		if d, ok := m.modal.(*newSession); ok {
			d.starting = false
		}
		if m.prompt != nil {
			m.prompt.starting = false
		}
		if errors.Is(msg.err, context.Canceled) {
			return nil
		}
		return m.toastErr(msg.err)
	}
	h.tier, h.tierUnset = msg.tier, msg.pending
	return msg.next()
}

type hostTierQuestion struct {
	request  tierQuestionRequest
	previous modal
	pick     int
}

func (q *hostTierQuestion) key(m *Model, key tea.KeyPressMsg) tea.Cmd {
	switch key.String() {
	case "esc", "ctrl+c":
		q.request.answer <- tierAnswer{err: context.Canceled}
		m.modal = q.previous
		return m.waitForTierQuestion()
	case "up", "k":
		q.pick = max(q.pick-1, 0)
	case "down", "j":
		q.pick = min(q.pick+1, len(tierChoices)-1)
	case "1", "2", "3":
		q.pick = int(key.String()[0] - '1')
		return q.finish(m)
	case "enter":
		return q.finish(m)
	}
	return nil
}

func (q *hostTierQuestion) finish(m *Model) tea.Cmd {
	if q.pick < 0 || q.pick >= len(tierChoices) {
		return nil
	}
	q.request.answer <- tierAnswer{tier: tierChoices[q.pick].tier}
	m.modal = q.previous
	return m.waitForTierQuestion()
}

func (q *hostTierQuestion) view(m *Model) string {
	body, hints := tierQuestionView(q.request.host.Name, q.pick, m.dialogWidth()-4)
	return sheet(m.dialogWidth(), "Choose a host tier", "", hints, trimBlank(body), toneNormal)
}
