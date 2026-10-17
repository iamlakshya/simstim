package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// The resume of a session whose agent has ended (docs/contributing/ux-spec.md
// 2.4.1). The host starts the agent again in the same session. The launcher
// then attaches, as after a start.

// resumedMsg is how a resume went.
type resumedMsg struct {
	key sessKey
	res *client.ResumeResult
	err error
}

// resumeSelected starts the selected session's agent again, with the cap
// it asked for, and the secrets it started with, fetched again. While a
// resume of the session runs, the key does nothing. The row still says
// that the agent ended until the host has stopped the old holder.
func (m *Model) resumeSelected() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	key := sessKey{h.name(), s.ID}
	if m.resuming[key] {
		return nil
	}
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	names, err := client.ResumeSecrets(h.host, *s, nil)
	if err != nil {
		// The host names the secrets: one that hosts.toml does not give it
		// needs the command line, where the user names it too.
		return m.toastErr(err)
	}
	if m.resuming == nil {
		m.resuming = map[sessKey]bool{}
	}
	m.resuming[key] = true
	c, ctx, be := h.conn, m.ctx, m.be
	resume := func() tea.Msg {
		// The host stops the old holder and its unit, then waits for the start.
		cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		secrets, err := be.SessionSecrets(cctx, names)
		if err != nil {
			return resumedMsg{key: key, err: err}
		}
		res, err := c.Resume(cctx, client.ResumeArgs{ID: key.id, Secrets: secrets})
		return resumedMsg{key: key, res: res, err: err}
	}
	text := fmt.Sprintf("Resuming %s on %s…", s.Name, h.name())
	if client.CapStopped(*s) && s.MemoryMax > 0 {
		// The cap that stopped the agent applies again, unless the command
		// line gives another.
		text = fmt.Sprintf("Resuming %s on %s, with its cap of %s…", s.Name, h.name(), fmtSize(s.MemoryMax))
	}
	return tea.Batch(m.toast(toastInfo, text), resume)
}

// onResumed shows the session's new row, and attaches to it while it is
// still the one selected and nothing else is open. Otherwise a notice says
// that it runs again.
func (m *Model) onResumed(msg resumedMsg) tea.Cmd {
	delete(m.resuming, msg.key)
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	h := m.hostByName(msg.key.host)
	if h == nil || msg.res == nil {
		return nil
	}
	s := msg.res.Session
	for i := range h.sessions {
		if h.sessions[i].ID == s.ID {
			h.sessions[i] = s
		}
	}
	h.changes++
	// The new holder's output starts again from its first byte, so what the
	// terminal kept of the old one cannot be replayed.
	if m.attach != nil && m.attach.Key == h.name()+"/"+s.ID {
		m.attach.Valid = false
	}
	if m.sel == msg.key && m.modal == nil && m.prompt == nil && m.rename == nil {
		return m.jack(h, s.ID, s.Name)
	}
	return tea.Batch(m.toast(toastSuccess, fmt.Sprintf("Resumed %s on %s.", s.Name, h.name())), m.listSessions(h))
}
