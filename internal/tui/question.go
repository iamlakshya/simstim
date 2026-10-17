package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// The host can answer a start with a question (setup.go, folder.go). The
// question keeps the start, and the user's answer sends it again, or
// cancels it.

// startReq is one start of a session, from the new-session form or from the
// quick-start line. That form or line waits for the start, and shows how it
// went.
type startReq struct {
	host string
	// args are what to start. They keep the secrets of the start, so that
	// the start after an answer does not fetch them again.
	args   client.NewArgs
	attach bool
	form   *newSession  // the form that started it, or nil
	line   *promptState // the quick-start line that started it, or nil
}

// waiting returns the form or the quick-start line that started r, while it
// still waits for r. Both are nil after the user cancels the start with esc.
func (m *Model) waiting(r startReq) (*newSession, *promptState) {
	switch {
	case r.form != nil && r.form.starting && m.modal == r.form:
		return r.form, nil
	case r.line != nil && r.line.starting && m.prompt == r.line:
		return nil, r.line
	}
	return nil, nil
}

// startAgain closes a question about a start, and sends the start again with
// the answer in r.args. The form comes back, still starting, until the reply.
// When the host is gone, the start ends, and a toast says why.
func (m *Model) startAgain(r startReq) tea.Cmd {
	h := m.hostByName(r.host)
	if h == nil {
		m.cancelStart(r)
		return m.toastErr(&client.UserError{Msg: "the host " + r.host + " is no longer in the list", Hint: "Add the host again, then start the session."})
	}
	m.modal = nil
	if r.form != nil {
		m.modal = r.form
	}
	return m.startSession(h, r)
}

// cancelStart closes a question about a start, and brings back what started
// it, ready to start again: the new-session form, or the quick-start line.
func (m *Model) cancelStart(r startReq) {
	m.modal = nil
	if r.form != nil {
		r.form.starting = false
		m.modal = r.form
	}
	if r.line != nil {
		r.line.starting = false
	}
}
