package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// setupAsk asks whether a repository's setup script may run, when a start
// meets a repository the user has not answered for on this host. The answer
// is kept, and the start runs again with it.
type setupAsk struct {
	host   string
	q      client.SetupQuestion
	args   client.NewArgs
	attach bool
	// back is the new-session form that started this, if any; it comes back
	// while the start runs again, or when the user cancels.
	back *newSession
}

// askSetup opens the question, in place of the form that started it.
func (m *Model) askSetup(msg startedMsg, q client.SetupQuestion) tea.Cmd {
	back, _ := m.modal.(*newSession)
	m.modal = &setupAsk{host: msg.host, q: q, args: msg.args, attach: msg.attach, back: back}
	return nil
}

func (s *setupAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	var run bool
	switch k.String() {
	case "y":
		run = true
	case "n":
		run = false
	case "esc", "q":
		m.cancelStart(s.back)
		return nil
	default:
		return nil
	}
	h := m.answered(s.host, s.back)
	if h == nil {
		return nil
	}
	a := s.args
	a.Setup = api.SetupSkip
	if run {
		a.Setup = api.SetupRun
	}
	be, host, repo := m.be, s.host, s.q.Repo
	remember := func() tea.Msg {
		if err := be.RememberSetup(host, repo, run); err != nil {
			return opDoneMsg{host: host, err: err}
		}
		return nil
	}
	return tea.Batch(remember, m.startSession(h, a, s.attach))
}

// cancelStart closes a question about a start, and brings back what started
// it: the new-session form, or the quick-start line, ready to start again.
func (m *Model) cancelStart(back *newSession) {
	m.modal = nil
	if back != nil {
		back.starting = false
		m.modal = back
	}
	if p := m.prompt; p != nil {
		p.starting = false
	}
}

// answered closes a question about a start that will run again, and returns
// its host, or nil if the host is gone.
func (m *Model) answered(host string, back *newSession) *hostConn {
	m.modal = nil
	if back != nil {
		m.modal = back // still starting: the start's answer closes it
	}
	return m.hostByName(host)
}

func (s *setupAsk) view(m *Model) string {
	w := m.dialogWidth()
	h := m.hostByName(s.host)
	repo, script := s.q.Repo, s.q.Script
	if h != nil {
		repo, script = homePath(h, repo), homePath(h, script)
	}
	body := make([]string, 1, 12) // a blank line first
	for _, l := range []string{
		"The repository " + repo + " on " + s.host + " has a setup script, " + script + ".",
		"It runs in the session before the agent, as you, with the session's environment and secrets. Say yes only for a repository you trust.",
		"The answer is kept for this repository on " + s.host + ".",
	} {
		body = append(body, wrap(l, w-4)...)
		body = append(body, "")
	}
	return sheet(w, "Run its setup script?", "", []hint{{"y", "run it"}, {"n", "skip it"}, {"esc", "cancel"}}, trimBlank(body), toneNormal)
}
