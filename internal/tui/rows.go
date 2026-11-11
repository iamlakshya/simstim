package tui

import (
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
)

type rowKind int

const (
	rowSession rowKind = iota
	rowHeader
	rowNote
)

// sessRow is one row of the sessions list.
type sessRow struct {
	kind rowKind
	h    *hostConn
	s    client.Session
}

func (r sessRow) key() sessKey { return sessKey{r.h.name(), r.s.ID} }

func (m *Model) sessionsOf(h *hostConn) []client.Session {
	var out []client.Session
	for _, s := range h.sessions {
		if m.hideExited && ended(s) {
			continue
		}
		out = append(out, s)
	}
	sortSessions(out)
	return out
}

func matchSession(q string, row client.Session) bool {
	s := shown(row)
	if _, ok := fuzzy(q, s.Name); ok {
		return true
	}
	lq := strings.ToLower(q)
	return strings.HasPrefix(s.ID, q) || strings.Contains(strings.ToLower(s.Agent), lq) || strings.Contains(strings.ToLower(s.Cwd), lq)
}

// rows lists the sessions screen: one host's sessions, or every host's
// grouped under its name.
func (m *Model) rows() []sessRow {
	if m.screen != scrSessions {
		return nil
	}
	if h := m.selectedHost(); h != nil {
		var out []sessRow
		for _, s := range m.sessionsOf(h) {
			out = append(out, sessRow{kind: rowSession, h: h, s: s})
		}
		return out
	}
	var out []sessRow
	for _, h := range m.hosts {
		list := m.sessionsOf(h)
		switch {
		case len(list) > 0:
			out = append(out, sessRow{kind: rowHeader, h: h})
			for _, s := range list {
				out = append(out, sessRow{kind: rowSession, h: h, s: s})
			}
		case h.status == stFailed:
			out = append(out, sessRow{kind: rowHeader, h: h}, sessRow{kind: rowNote, h: h})
		case h.status == stOffline:
			out = append(out, sessRow{kind: rowHeader, h: h})
		}
	}
	return out
}

func sessionRows(rows []sessRow) int {
	n := 0
	for _, r := range rows {
		if r.kind == rowSession {
			n++
		}
	}
	return n
}

// selected is the selected session, or nil.
func (m *Model) selected() *client.Session {
	h, s := m.selectedPair()
	if h == nil {
		return nil
	}
	return s
}

func (m *Model) selectedPair() (*hostConn, *client.Session) {
	if m.screen != scrSessions || m.sel == (sessKey{}) {
		return nil, nil
	}
	h := m.hostByName(m.sel.host)
	if h == nil {
		return nil, nil
	}
	for i := range h.sessions {
		if h.sessions[i].ID == m.sel.id {
			return h, &h.sessions[i]
		}
	}
	return nil, nil
}

// syncSel keeps the selection on the same session after the list changes,
// or on the nearest one if it went away. A wanted session (the one last
// attached to) is taken as soon as it is listed.
func (m *Model) syncSel() {
	m.hostSel = max(min(m.hostSel, len(m.visibleHosts())-1), 0)
	rows := m.rows()
	find := func(k sessKey) int {
		for i, r := range rows {
			if r.kind == rowSession && r.key() == k {
				return i
			}
		}
		return -1
	}
	idx := -1
	if m.want != (sessKey{}) {
		if idx = find(m.want); idx >= 0 {
			m.want, m.autoSel = sessKey{}, false
		}
	}
	if idx < 0 {
		idx = find(m.sel)
		switch {
		case m.autoSel:
			idx = nearestSession(rows, 0)
		case idx < 0:
			idx = nearestSession(rows, m.selIdx)
		}
	}
	m.selIdx = max(idx, 0)
	if idx >= 0 {
		m.sel = rows[idx].key()
	} else {
		m.sel = sessKey{}
	}
	m.scrollTo()
}

func nearestSession(rows []sessRow, i int) int {
	if len(rows) == 0 {
		return -1
	}
	i = min(max(i, 0), len(rows)-1)
	for j := i; j < len(rows); j++ {
		if rows[j].kind == rowSession {
			return j
		}
	}
	for j := i; j >= 0; j-- {
		if rows[j].kind == rowSession {
			return j
		}
	}
	return -1
}

// scrollTo keeps the selected row in view.
func (m *Model) scrollTo() {
	listH := m.listRoom()
	if m.screen == scrHosts {
		n := len(m.visibleHosts())
		if m.hostSel < m.hostTop {
			m.hostTop = m.hostSel
		}
		if m.hostSel >= m.hostTop+listH {
			m.hostTop = m.hostSel - listH + 1
		}
		m.hostTop = max(0, min(m.hostTop, n-listH))
		return
	}
	n := len(m.rows())
	if m.selIdx < m.sessTop {
		m.sessTop = m.selIdx
	}
	if m.selIdx >= m.sessTop+listH {
		m.sessTop = m.selIdx - listH + 1
	}
	m.sessTop = max(0, min(m.sessTop, n-listH))
	// A group header just above the first row stays in view.
	if m.sessTop == 1 && m.selIdx == 1 {
		m.sessTop = 0
	}
}

// moveSel moves the session selection by d rows, skipping headers.
func (m *Model) moveSel(d int) {
	rows := m.rows()
	if len(rows) == 0 {
		return
	}
	i := m.selIdx
	for {
		i += d
		if i < 0 || i >= len(rows) {
			return
		}
		if rows[i].kind == rowSession {
			break
		}
	}
	m.selIdx, m.sel, m.autoSel, m.want = i, rows[i].key(), false, sessKey{}
	m.scrollTo()
}

func (m *Model) jumpSel(d int) {
	rows := m.rows()
	target := min(max(m.selIdx+d, 0), len(rows)-1)
	if i := nearestSession(rows, target); i >= 0 {
		m.selIdx, m.sel, m.autoSel = i, rows[i].key(), false
		m.scrollTo()
	}
}

func (m *Model) moveHost(d int) {
	n := len(m.visibleHosts())
	m.hostSel = min(max(m.hostSel+d, 0), max(n-1, 0))
	m.scrollTo()
}

// openHost shows the sessions of h, or of every host for nil.
func (m *Model) openHost(h *hostConn) {
	m.screen, m.cur = scrSessions, ""
	if h != nil {
		m.cur = h.name()
	}
	m.prompt = nil
	m.selIdx, m.sessTop, m.autoSel = 0, 0, true
	m.sel = sessKey{}
	m.syncSel()
}

// backToHosts returns to the hosts list, on the host that was open.
func (m *Model) backToHosts() {
	cur := m.cur
	m.screen, m.sel, m.rename, m.prompt = scrHosts, sessKey{}, nil, nil
	for i, h := range m.visibleHosts() {
		if h.name() == cur {
			m.hostSel = i
		}
	}
	m.syncSel()
}
