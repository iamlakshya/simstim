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

// startSession sends "new" to the host h, for the start r.
func (m *Model) startSession(h *hostConn, r startReq) tea.Cmd {
	r.host = h.name()
	if !h.online() {
		err := offlineErr(h)
		return func() tea.Msg { return startedMsg{startReq: r, err: err} }
	}
	r.args = client.TerminalArgs(r.args, m.width, m.height)
	c, ctx, be, host := h.conn, m.ctx, m.be, h.host
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		if r.args.Setup == "" {
			r.args.SetupTrust = be.SetupTrust(r.host)
		}
		// One fetch of the secrets for each start. A question about the
		// start keeps them for the start after the answer.
		if r.args.Secrets == nil {
			secrets, err := be.Secrets(cctx, host)
			if err != nil {
				return startedMsg{startReq: r, err: err}
			}
			r.args.Secrets = secrets
		}
		s, err := c.NewSession(cctx, r.args)
		return startedMsg{startReq: r, s: s, err: err}
	}
}

// onStarted shows how a start went, in the form or the quick-start line that
// waits for it, or opens the host's question about it. After esc, nothing
// waits: the launcher asks nothing, and does not attach to the session.
func (m *Model) onStarted(msg startedMsg) tea.Cmd {
	d, p := m.waiting(msg.startReq)
	waited := d != nil || p != nil
	if q, ok := client.FolderQuestionOf(msg.err); ok && !msg.args.MakeFolder {
		if waited {
			m.modal = &makeFolderAsk{startReq: msg.startReq, q: q}
		}
		return nil
	}
	if q, ok := client.SetupQuestionOf(msg.err); ok {
		if waited {
			m.modal = &setupAsk{startReq: msg.startReq, q: q}
		}
		return nil
	}
	if msg.err != nil {
		if d != nil {
			d.starting, d.err = false, msg.err
			return nil
		}
		if p != nil {
			p.starting, p.err = false, msg.err
		}
		return m.toastErr(msg.err)
	}
	if d != nil {
		m.lastAgent[msg.host] = d.agent()
		m.modal = nil
	}
	if p != nil {
		m.prompt = nil
	}
	h := m.hostByName(msg.host)
	if h == nil || msg.s == nil {
		return nil
	}
	h.sessions = append(h.sessions, *msg.s)
	h.changes++
	if waited {
		if m.screen != scrSessions || m.cur != "" && m.cur != h.name() {
			m.openHost(h)
		}
		m.sel, m.autoSel = sessKey{h.name(), msg.s.ID}, false
		m.syncSel()
		if msg.attach {
			return m.jack(h, msg.s.ID, msg.s.Name)
		}
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
