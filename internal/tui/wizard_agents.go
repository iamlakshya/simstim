package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// The agent part of the wizard (docs/contributing/ux-spec.md 2.10): after
// "ready", the checklist, each install and each login in a session the user
// watches, and a summary. onboard.Flow decides what comes next; the wizard
// only shows it and runs it.

type agentPart struct {
	flow     *onboard.Flow
	checking bool // the host's agent checks are on their way
	row      int  // the selected row of the checklist
	unfold   bool // the best-effort agents show
	act      onboard.Action
	starting bool          // the session of act is starting
	waiting  string        // the session the wizard waits on, while attached
	last     *onboard.Line // how the last install or login went
}

type (
	agentChecksMsg struct {
		seq    int
		checks []client.AgentCheck
		err    error
		act    onboard.Action // the install or login the checks follow, if any
		ended  bool
	}
	actStartedMsg struct {
		seq int
		s   *client.Session
		err error
	}
)

// wantsAgents reports whether the checklist follows "ready": always in the
// guided setup, and otherwise when an agent is missing or not logged in.
func (w *wizard) wantsAgents() bool {
	f := w.flow
	if f == nil {
		return false
	}
	if w.guided || f.Missing() {
		return true
	}
	for _, it := range f.Items {
		if onboard.NeedsLogin(it) {
			return true
		}
	}
	return false
}

// loadAgents asks the new host about its agents.
func (w *wizard) loadAgents(m *Model) tea.Cmd { return w.recheck(m, onboard.Action{}, false) }

// recheck asks the host about its agents, after an install or a login.
func (w *wizard) recheck(m *Model, a onboard.Action, ended bool) tea.Cmd {
	h := m.hostByName(w.host.Name)
	if h == nil || !h.online() {
		if a.Kind != "" {
			w.err = w.hostErr(h)
		}
		return nil
	}
	w.checking = true
	c, ctx, seq := h.conn, m.ctx, w.seq
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		checks, err := c.AgentChecks(cctx)
		return agentChecksMsg{seq: seq, checks: checks, err: err, act: a, ended: ended}
	}
}

func (w *wizard) onAgentChecks(m *Model, msg agentChecksMsg) tea.Cmd {
	if msg.seq != w.seq {
		return nil
	}
	w.checking = false
	if msg.err != nil {
		// An older helper cannot check agents: the wizard ends at "ready".
		if msg.act.Kind != "" {
			w.err = msg.err
		}
		return nil
	}
	if w.flow == nil {
		w.flow = onboard.NewFlow(w.host.Name, msg.checks, nil)
		return nil
	}
	w.flow.Done(msg.act, msg.checks, msg.ended)
	l := w.flow.Line(msg.act.Agent.Kind)
	w.last = &l
	var cmd tea.Cmd
	if h := m.hostByName(w.host.Name); h != nil && msg.act.Kind == onboard.ActInstall {
		cmd = m.doctor(h) // the new-session form lists the host's agents
	}
	return tea.Batch(cmd, w.next())
}

// next shows what the flow does next.
func (w *wizard) next() tea.Cmd {
	w.act, w.err = w.flow.Next(), nil
	w.step = wzRun
	if w.act.Kind == onboard.ActDone {
		w.step = wzDone
	}
	return nil
}

// rows are the checklist's rows: indexes into the flow's items, and -1 for
// the fold that holds the best-effort agents.
func (w *wizard) rows() []int {
	var out []int
	folded := false
	for i, it := range w.flow.Items {
		switch {
		case !it.BestEffort() || w.unfold:
			out = append(out, i)
		case !folded:
			folded = true
			out = append(out, -1)
		}
	}
	return out
}

func (w *wizard) agentKey(m *Model, k tea.KeyPressMsg) tea.Cmd {
	ks := k.String()
	switch w.step {
	case wzAgents:
		if w.flow == nil {
			return nil
		}
		rows := w.rows()
		w.row = min(w.row, len(rows)-1)
		switch ks {
		case "up", "k":
			w.row = max(w.row-1, 0)
		case "down", "j":
			w.row = min(w.row+1, len(rows)-1)
		case "space", "x":
			if rows[w.row] < 0 {
				w.unfold = true
			} else {
				w.flow.Toggle(rows[w.row])
			}
		case "enter":
			if rows[w.row] < 0 {
				w.unfold = true
				return nil
			}
			w.flow.Start()
			return w.next()
		}
	case wzRun:
		if w.starting || w.checking || w.waiting != "" {
			return nil
		}
		switch ks {
		case "enter":
			return w.runAct(m)
		case "s":
			w.flow.Skip(w.act)
			w.last = nil
			return w.next()
		}
	case wzDone:
		if ks == "enter" {
			w.startSession(m, w.flow.FirstAgent())
		}
	default:
		// The earlier steps take their keys in wizard.key.
	}
	return nil
}

// agentBack is Esc in the agent part: the checklist goes back to "ready",
// an install or a login stops the rest, and the summary closes.
func (w *wizard) agentBack(m *Model) tea.Cmd {
	switch w.step {
	case wzAgents:
		w.step = wzReady
	case wzRun:
		if w.starting || w.checking || w.waiting != "" {
			return nil
		}
		w.flow.Stop()
		w.last = nil
		w.step = wzDone
	default:
		m.modal = nil
	}
	return nil
}

// runAct starts the install or the login in a session, then attaches.
func (w *wizard) runAct(m *Model) tea.Cmd {
	h := m.hostByName(w.host.Name)
	if h == nil || !h.online() {
		w.err = w.hostErr(h)
		return nil
	}
	w.starting, w.err = true, nil
	a := client.TerminalArgs(w.act.Args, m.width, m.height)
	c, ctx, seq := h.conn, m.ctx, w.seq
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		s, err := c.NewSession(cctx, a)
		return actStartedMsg{seq: seq, s: s, err: err}
	}
}

func (w *wizard) onActStarted(m *Model, msg actStartedMsg) tea.Cmd {
	if msg.seq != w.seq {
		return nil
	}
	w.starting = false
	h := m.hostByName(w.host.Name)
	if msg.err != nil || msg.s == nil || h == nil {
		w.err = msg.err
		return nil
	}
	h.sessions = append(h.sessions, *msg.s)
	h.changes++
	w.waiting, w.act.Session = msg.s.ID, msg.s.ID
	return m.attachTo(h, msg.s.ID, msg.s.Name)
}

// afterAttach checks the host again when the user leaves the session.
func (w *wizard) afterAttach(m *Model, msg attachDoneMsg) tea.Cmd {
	w.waiting = ""
	if msg.err != nil {
		w.err = msg.err
		return nil
	}
	return w.recheck(m, w.act, msg.res.Ended != nil)
}

// startSession closes the wizard and opens the new-session form on the
// host, with agent chosen when it is set.
func (w *wizard) startSession(m *Model, agent string) {
	h := m.hostByName(w.host.Name)
	m.modal = nil
	if h == nil {
		return
	}
	m.openHost(h)
	if agent != "" {
		m.lastAgent[h.name()] = agent
	}
	m.modal = newSessionDialog(m, h)
}

// hostErr says why the wizard's host cannot be reached now.
func (w *wizard) hostErr(h *hostConn) error {
	if h == nil {
		return &client.UserError{Msg: w.host.Name + " is no longer in the list", Hint: "Close this, and add the host again.", Code: "host_not_found", Kind: client.KindNotFound}
	}
	return offlineErr(h)
}
