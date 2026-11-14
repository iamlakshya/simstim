package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// onKey routes a key: the quit chord, a jack-in under way, then the sheet on
// top, the prompt line, an inline rename, then the numbers, the moves and the
// command registry.
func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	ks := k.String()
	if ks == "ctrl+c" {
		if q, ok := m.modal.(*hostTierQuestion); ok {
			return q.key(m, k)
		}
	}
	if m.quitArmed {
		// ctrl+c again quits, anything else cancels.
		m.quitArmed = false
		if ks == "ctrl+c" {
			return tea.Quit
		}
		return nil
	}
	if ks == "ctrl+c" {
		m.quitArmed = true
		return nil
	}
	if m.modal != nil {
		return m.modal.key(m, k)
	}
	if m.prompt != nil {
		return m.promptKey(k)
	}
	if m.rename != nil {
		return m.renameKey(k)
	}
	if len(m.hosts) == 0 && ks == "enter" {
		return m.openGuide() // the welcome's first key
	}
	if len(m.hosts) > 0 && len(ks) == 1 && ks >= "1" && ks <= "9" {
		return m.quickOpen(int(ks[0] - '0'))
	}
	if len(m.hosts) > 0 && m.navKey(ks) {
		return nil
	}
	for _, c := range commands {
		if c.matches(m, ks) {
			return m.runCommand(c)
		}
	}
	return nil
}

// navKey moves through the list, and between the two screens. It reports
// whether it used the key.
func (m *Model) navKey(ks string) bool {
	switch ks {
	case "up", "k", "down", "j", "home", "end", "pgup", "pgdown":
		d := map[string]int{"up": -1, "k": -1, "down": 1, "j": 1, "home": -1 << 20, "end": 1 << 20, "pgup": -10, "pgdown": 10}[ks]
		switch {
		case m.screen == scrHosts:
			m.moveHost(d)
		case d == 1 || d == -1:
			m.moveSel(d)
		default:
			m.jumpSel(d)
		}
	case "right", "l":
		if m.screen != scrHosts || m.selectedHost() == nil {
			return false
		}
		m.openHost(m.selectedHost())
	case "left", "h", "esc":
		if m.screen != scrSessions {
			return false
		}
		m.backToHosts()
	default:
		return false
	}
	return true
}

// onPaste handles a paste: into a field being typed, or, if it is a session
// ID, a jump to that session.
func (m *Model) onPaste(text string) tea.Cmd {
	if m.transferInputBlocked() {
		return nil
	}
	switch {
	case m.modal != nil:
		if p, ok := m.modal.(interface{ paste(string) }); ok {
			p.paste(text)
		}
		return nil
	case m.prompt != nil:
		m.prompt.in.insert(text)
		return nil
	case m.rename != nil:
		before := m.rename.in.value()
		m.rename.in.insert(text)
		if m.rename.in.value() != before {
			m.rename.edited = true
		}
		return nil
	}
	id := strings.TrimSpace(text)
	if _, ref, err := client.SplitTarget(id); err == nil {
		id = ref
	}
	for _, h := range m.hosts {
		for _, s := range h.sessions {
			if s.ID == id || (len(id) >= 4 && strings.HasPrefix(s.ID, id)) {
				m.openHost(h)
				m.sel, m.autoSel = sessKey{h.name(), s.ID}, false
				m.syncSel()
				return nil
			}
		}
	}
	return m.toast(toastInfo, fmt.Sprintf("No session with the ID %s.", trunc(id, 24)))
}

// Rename.

func (m *Model) startRename() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	m.rename = &renameState{key: sessKey{h.name(), s.ID}, old: s.Name, in: newInput(shown(s.Name), "a name")}
	return nil
}

func (m *Model) renameKey(k tea.KeyPressMsg) tea.Cmd {
	r := m.rename
	if r.busy {
		return nil
	}
	switch k.String() {
	case "esc":
		m.rename = nil
		return nil
	case "enter":
		name := strings.TrimSpace(r.in.value())
		h := m.hostByName(r.key.host)
		if name == "" || name == r.old || !r.edited && name == strings.TrimSpace(shown(r.old)) || h == nil {
			m.rename = nil
			return nil
		}
		// A name that looks like an ID is refused, and the box stays open.
		if err := client.CheckSessionName(name); err != nil {
			return m.toastErr(err)
		}
		r.busy = true
		h.changes++
		for i := range h.sessions {
			if h.sessions[i].ID == r.key.id {
				h.sessions[i].Name = name // show it now; the next list confirms it
			}
		}
		id, old := r.key.id, r.old
		return m.op(h, fmt.Sprintf("Renamed %s to %s on %s.", shown(old), shown(name), shown(h.name())), func(ctx context.Context, c Conn) error {
			return c.RenameSession(ctx, id, name)
		})
	}
	before := r.in.value()
	if r.in.key(k) && r.in.value() != before {
		r.edited = true
	}
	return nil
}
