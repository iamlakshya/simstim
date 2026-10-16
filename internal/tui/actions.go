package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// op runs one request on h's connection and reports with a toast.
func (m *Model) op(h *hostConn, ok string, fn func(context.Context, Conn) error) tea.Cmd {
	if !h.online() {
		if m.rename != nil {
			m.rename = nil
		}
		return m.toastErr(offlineErr(h))
	}
	name, c, ctx := h.name(), h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return opDoneMsg{host: name, ok: ok, err: fn(cctx, c)}
	}
}

func offlineErr(h *hostConn) error {
	if h.status == stConnecting {
		return &client.UserError{Msg: "still connecting to " + h.name(), Hint: "Wait a moment, then try again."}
	}
	return &client.UserError{Msg: h.name() + " is offline", Hint: fmt.Sprintf("%s host test %s", brand.Name, h.name())}
}

// newSessionHost picks the host for "session: new": the host in play, or
// on the list of every host, the selected session's host.
func (m *Model) newSessionHost() *hostConn {
	if h := m.selectedHost(); h != nil {
		return h
	}
	if h, _ := m.selectedPair(); h != nil {
		return h
	}
	if len(m.hosts) == 1 {
		return m.hosts[0]
	}
	return nil
}

func (m *Model) openNewSession() tea.Cmd {
	h := m.newSessionHost()
	if h == nil {
		m.backToHosts()
		return m.toast(toastInfo, "Choose a host, then press n.")
	}
	m.modal = newSessionDialog(m, h)
	return nil
}

// openSelectedHost is Enter on the hosts list.
func (m *Model) openSelectedHost() tea.Cmd {
	if h := m.selectedHost(); h != nil {
		m.openHost(h)
	}
	return nil
}

// startSession sends "new" to the host.
func (m *Model) startSession(h *hostConn, a client.NewArgs, attach bool) tea.Cmd {
	if !h.online() {
		return func() tea.Msg { return startedMsg{host: h.name(), err: offlineErr(h)} }
	}
	a = client.TerminalArgs(a, m.width, m.height)
	name, c, ctx, be, host := h.name(), h.conn, m.ctx, m.be, h.host
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if a.Setup == "" {
			a.SetupTrust = be.SetupTrust(name)
		}
		secrets, err := be.Secrets(cctx, host)
		if err != nil {
			return startedMsg{host: name, err: err, attach: attach, args: a}
		}
		a.Secrets = secrets
		s, err := c.NewSession(cctx, a)
		a.Secrets = nil // fetched again for a new start; never kept
		return startedMsg{host: name, s: s, err: err, attach: attach, args: a}
	}
}

func (m *Model) onStarted(msg startedMsg) tea.Cmd {
	if q, ok := client.FolderQuestionOf(msg.err); ok && !msg.args.MakeFolder {
		return m.askFolder(msg, q)
	}
	if q, ok := client.SetupQuestionOf(msg.err); ok {
		return m.askSetup(msg, q)
	}
	d, _ := m.modal.(*newSession)
	if msg.err != nil {
		if d != nil {
			d.starting, d.err = false, msg.err
			return nil
		}
		if p := m.prompt; p != nil && p.starting {
			p.starting, p.err = false, msg.err
		}
		return m.toastErr(msg.err)
	}
	if d != nil {
		m.lastAgent[msg.host] = d.agent()
		m.modal = nil
	}
	h := m.hostByName(msg.host)
	if h == nil || msg.s == nil {
		return nil
	}
	h.sessions = append(h.sessions, *msg.s)
	h.changes++
	if m.screen != scrSessions || m.cur != "" && m.cur != h.name() {
		m.openHost(h)
	}
	m.sel, m.autoSel = sessKey{h.name(), msg.s.ID}, false
	m.syncSel()
	if p := m.prompt; p != nil && p.starting {
		m.prompt = nil
	}
	if msg.attach {
		return m.jack(h, msg.s.ID, msg.s.Name)
	}
	return tea.Batch(m.toast(toastSuccess, fmt.Sprintf("Started %s on %s.", msg.s.Name, h.name())), m.listSessions(h))
}

func (m *Model) attachSelected() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		if sessionRows(m.rows()) == 0 {
			return m.openQuickStart()
		}
		return nil
	}
	if !s.Alive {
		return m.toastErr(&client.UserError{
			Msg:  fmt.Sprintf("%s on %s has ended", s.Name, h.name()),
			Hint: "Press d to remove it, or n to start a new one.",
		})
	}
	return m.jack(h, s.ID, s.Name)
}

func (m *Model) copyAttach() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	ref := s.ID
	if s.Name != "" && !strings.ContainsAny(s.Name, " /'\"") {
		n := 0
		for _, o := range h.sessions {
			if o.Name == s.Name {
				n++
			}
		}
		if n == 1 {
			ref = s.Name
		}
	}
	text := fmt.Sprintf("%s attach %s/%s", brand.Name, h.name(), ref)
	return tea.Batch(tea.SetClipboard(text), m.toast(toastSuccess, "Copied: "+text+"."))
}

func (m *Model) confirmDelete() tea.Cmd {
	h, s := m.selectedPair()
	if s == nil {
		return nil
	}
	m.modal = deleteDialog(h, *s)
	return nil
}

// deleteSession runs after the confirm.
func (m *Model) deleteSession(host string, s client.Session) tea.Cmd {
	h := m.hostByName(host)
	if h == nil {
		return nil
	}
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	id, c, ctx := s.ID, h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		res, err := c.Delete(cctx, id)
		ok := fmt.Sprintf("Deleted %s on %s.", s.Name, host)
		if res.Worktree != "" {
			ok += " " + sentence(res.Worktree)
		}
		return opDoneMsg{host: host, ok: ok, err: err, drop: id}
	}
}

// saveHosts writes the host list with one host added, replaced or removed.
func (m *Model) saveHosts(host client.Host, remove bool) tea.Cmd {
	be := m.be
	return func() tea.Msg {
		hosts, err := be.LoadHosts()
		if err != nil {
			return hostSavedMsg{host: host, remove: remove, err: err}
		}
		out := hosts[:0]
		for _, h := range hosts {
			if h.Name != host.Name {
				out = append(out, h)
			}
		}
		if !remove {
			out = append(out, host)
		}
		return hostSavedMsg{host: host, remove: remove, err: be.SaveHosts(out)}
	}
}

func (m *Model) onHostSaved(msg hostSavedMsg) tea.Cmd {
	if w, ok := m.modal.(*wizard); ok && !msg.remove {
		return w.onMsg(m, msg)
	}
	if msg.err != nil {
		return m.toastErr(msg.err)
	}
	if msg.remove {
		return tea.Batch(m.removeHost(msg.host.Name),
			m.toast(toastSuccess, fmt.Sprintf("Forgot %s. Its sessions and the helper stay on the host.", msg.host.Name)))
	}
	return nil
}

// testHost measures the round trip to h.
func (m *Model) testHost(h *hostConn) tea.Cmd {
	if !h.online() {
		return m.toastErr(offlineErr(h))
	}
	name, c, ctx := h.name(), h.conn, m.ctx
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		d, err := c.RoundTrip(cctx)
		if err != nil {
			return opDoneMsg{host: name, err: err}
		}
		return opDoneMsg{host: name, ok: fmt.Sprintf("%s answers in %s.", name, fmtLatency(d))}
	}
}
