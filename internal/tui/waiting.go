package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
)

// nextWaiting selects the next session that waits for the user, on any host,
// after the one selected, so one key cycles through them (roadmap N3). It
// stays in the all-sessions view when that is open, and otherwise opens the
// session's host. Enter then joins it.
func (m *Model) nextWaiting() tea.Cmd {
	type waiting struct {
		h   *hostConn
		key sessKey
		msg string
	}
	var all []waiting
	for _, h := range m.hosts {
		for _, s := range m.sessionsOf(h) {
			if s.State != api.StateNeedsInput {
				continue
			}
			d := shown(s)
			msg := d.Name + " on " + h.name() + " needs you"
			if d.Reason != "" {
				msg += ": " + d.Reason
			}
			all = append(all, waiting{h, sessKey{h.name(), s.ID}, msg})
		}
	}
	if len(all) == 0 {
		return m.toast(toastInfo, "No session needs you.")
	}
	next := 0
	if m.screen == scrSessions {
		for i, w := range all {
			if w.key == m.sel {
				next = (i + 1) % len(all)
			}
		}
	}
	w := all[next]
	if m.screen != scrSessions || (m.cur != "" && m.cur != w.h.name()) {
		m.openHost(w.h)
	}
	m.want, m.autoSel = w.key, false
	m.syncSel()
	return m.toast(toastWarning, sentence(w.msg))
}
