package tui

import (
	"context"
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// onConn handles the connection messages.
func (m *Model) onConn(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case dialledMsg:
		return m.onDialled(msg)
	case connDoneMsg:
		return m.onConnDone(msg)
	case redialMsg:
		return m.onRedial(msg)
	case sessionsMsg:
		return m.onSessions(msg)
	case latencyMsg:
		return m.onLatency(msg)
	case doctorMsg:
		return m.onDoctor(msg)
	case refreshTickMsg:
		return m.onRefreshTick()
	case latencyTickMsg:
		return m.onLatencyTick()
	}
	return nil
}

// onDialled takes a finished dial: a live connection, or an error and a retry later.
func (m *Model) onDialled(msg dialledMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen {
		if msg.conn != nil {
			return m.dropConn(msg.conn)
		}
		return nil
	}
	if msg.err != nil {
		h.err = msg.err
		h.status = stFailed
		if offline(msg.err) {
			h.status = stOffline
		}
		h.backoff = nextBackoff(h.backoff)
		return m.after(h.backoff, redialMsg{name: h.name(), gen: h.gen})
	}
	h.conn, h.status, h.err, h.backoff = msg.conn, stOnline, nil, 0
	cmds := []tea.Cmd{m.listSessions(h), m.ping(h), m.doctor(h), waitDone(h.name(), h.gen, h.conn)}
	if m.previewOpen() && m.preview.key.host == h.name() {
		cmds = append(cmds, m.peek())
	}
	return tea.Batch(cmds...)
}

// onConnDone notes a lost connection and dials again later.
func (m *Model) onConnDone(msg connDoneMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen || h.conn == nil {
		return nil
	}
	old := h.conn
	h.conn, h.status, h.latency = nil, stOffline, 0
	h.err = &client.UserError{
		Msg:  "lost the connection to " + h.name(),
		Hint: fmt.Sprintf("%s host test %s", brand.Name, h.name()),
	}
	h.backoff = nextBackoff(h.backoff)
	return tea.Batch(m.dropConn(old), m.after(h.backoff, redialMsg{name: h.name(), gen: h.gen}))
}

// onRedial dials a host again, unless it is gone or back online.
func (m *Model) onRedial(msg redialMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen || h.online() {
		return nil
	}
	return m.dial(h)
}

// onSessions takes a host's list of sessions.
func (m *Model) onSessions(msg sessionsMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen {
		return nil
	}
	h.listing = false
	if msg.changes != h.changes {
		return m.listSessions(h) // asked for before a local edit: ask again
	}
	if msg.err != nil {
		if !errors.Is(msg.err, client.ErrClosed) && !errors.Is(msg.err, context.Canceled) {
			h.err = msg.err
		}
		return nil
	}
	h.err = nil
	cmd := m.alertNew(h, msg.list)
	m.noteActivity(h, msg.list)
	h.sessions, h.loaded = msg.list, true
	m.syncSel()
	if m.want.host == h.name() {
		m.want = sessKey{} // it is gone: the first live list did not have it
	}
	return cmd
}

// onLatency takes a ping's round trip.
func (m *Model) onLatency(msg latencyMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen {
		return nil
	}
	h.pinging = false
	if msg.d > 0 {
		h.latency = msg.d
	}
	return nil
}

// onDoctor keeps a host's facts, for the details and the new-session form.
func (m *Model) onDoctor(msg doctorMsg) tea.Cmd {
	h := m.hostByName(msg.name)
	if h == nil || h.gen != msg.gen || msg.err != nil {
		return nil
	}
	info := msg.info
	h.info = &info
	if d, ok := m.modal.(*newSession); ok {
		d.refresh(m)
	}
	return nil
}

// onRefreshTick lists every host's sessions again, and refreshes a peek.
func (m *Model) onRefreshTick() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2+len(m.hosts))
	cmds = append(cmds, m.after(refreshEvery, refreshTickMsg{}))
	for _, h := range m.hosts {
		cmds = append(cmds, m.listSessions(h))
	}
	if s := m.selected(); s != nil && !ended(*s) && m.previewOpen() { // an ended agent's screen stays as it is
		cmds = append(cmds, m.peek())
	}
	return tea.Batch(cmds...)
}

// onLatencyTick pings every host.
func (m *Model) onLatencyTick() tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2+len(m.hosts))
	cmds = append(cmds, m.after(latencyEvery, latencyTickMsg{}))
	for _, h := range m.hosts {
		cmds = append(cmds, m.ping(h))
	}
	return tea.Batch(cmds...)
}
