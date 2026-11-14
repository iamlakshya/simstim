package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// onOpDone reports a finished operation, and lists the host's sessions again.
func (m *Model) onOpDone(msg opDoneMsg) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case msg.err != nil:
		cmd = m.toastErr(msg.err)
	case msg.warn != "":
		cmd = m.toastNext(toastWarning, msg.warn, msg.hint)
	case msg.ok != "":
		cmd = m.toast(toastSuccess, msg.ok)
	}
	if h := m.hostByName(msg.host); h != nil {
		h.changes++
		if msg.err == nil && msg.drop != "" {
			h.sessions = slices.DeleteFunc(h.sessions, func(s client.Session) bool { return s.ID == msg.drop })
			m.syncSel()
		}
		cmd = tea.Batch(cmd, m.listSessions(h))
	}
	if m.rename != nil && m.rename.busy {
		m.rename = nil
	}
	return cmd
}
