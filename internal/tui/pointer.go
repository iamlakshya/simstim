package tui

import (
	tea "charm.land/bubbletea/v2"
)

// Quick keys and the mouse (docs/contributing/ux-spec.md 3). The keyboard stays the main way
// in; these make the common path shorter. 1–9 open the row with that number.
// A click selects a row, a click on the selected row opens it, and the wheel
// moves the selection.

// quickOpen opens the n-th row: a host's sessions, or attaches a session.
func (m *Model) quickOpen(n int) tea.Cmd {
	if m.screen == scrHosts {
		vh := m.visibleHosts()
		if n < 1 || n > len(vh) {
			return nil
		}
		m.hostSel = n - 1
		m.openHost(vh[n-1])
		return nil
	}
	i := m.nthSession(n)
	if i < 0 {
		return nil
	}
	rows := m.rows()
	m.selIdx, m.sel, m.autoSel, m.want = i, rows[i].key(), false, sessKey{}
	m.scrollTo()
	return m.attachSelected()
}

// nthSession is the list index of the n-th session row, or -1.
func (m *Model) nthSession(n int) int {
	for i, r := range m.rows() {
		if r.kind != rowSession {
			continue
		}
		if n--; n == 0 {
			return i
		}
	}
	return -1
}

func (m *Model) onClick(msg tea.MouseClickMsg) tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.quitArmed = false
	if m.modal != nil {
		return m.clickModal(mouse.X, mouse.Y)
	}
	if m.rename != nil || len(m.hosts) == 0 {
		return nil
	}
	g := m.geo
	if mouse.X < g.x || mouse.X >= g.x+g.w || mouse.Y < g.listY || mouse.Y >= g.listY+g.listN {
		return nil
	}
	i := g.top + mouse.Y - g.listY
	if m.screen == scrHosts {
		vh := m.visibleHosts()
		if i >= len(vh) {
			return nil
		}
		if i == m.hostSel {
			m.openHost(vh[i])
			return nil
		}
		m.hostSel = i
		return nil
	}
	rows := m.rows()
	if i >= len(rows) || rows[i].kind != rowSession {
		return nil
	}
	if i == m.selIdx && rows[i].key() == m.sel {
		return m.attachSelected()
	}
	m.selIdx, m.sel, m.autoSel, m.want = i, rows[i].key(), false, sessKey{}
	return nil
}

// clickModal handles a click on a dialog: a palette row runs, and a click
// outside a dialog that holds no typing closes it.
func (m *Model) clickModal(x, y int) tea.Cmd {
	g := m.modalGeo
	inside := x >= g.x && x < g.x+g.w && y >= g.y && y < g.y+g.h
	switch d := m.modal.(type) {
	case *palette:
		if !inside {
			m.modal = nil
			return nil
		}
		// The rows start under the border, a blank row, the filter and
		// another blank row.
		i := d.top + y - (g.y + 4)
		if y < g.y+4 || i >= len(d.items(m)) {
			return nil
		}
		d.sel = i
		return d.key(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	case *previewModal, *details, *confirm:
		if !inside {
			m.modal = nil
		}
	}
	return nil
}

func (m *Model) onWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	key := map[tea.MouseButton]string{tea.MouseWheelUp: "up", tea.MouseWheelDown: "down"}[msg.Mouse().Button]
	if key == "" {
		return nil
	}
	switch d := m.modal.(type) {
	case nil:
		if m.rename == nil && len(m.hosts) > 0 {
			m.navKey(key)
		}
	case *palette, *previewModal:
		return d.key(m, keyFor(key))
	}
	return nil
}

func keyFor(name string) tea.KeyPressMsg {
	if name == "up" {
		return tea.KeyPressMsg{Code: tea.KeyUp}
	}
	return tea.KeyPressMsg{Code: tea.KeyDown}
}
