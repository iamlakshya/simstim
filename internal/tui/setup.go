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
	startReq
	q client.SetupQuestion
}

func (s *setupAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	var run bool
	switch k.String() {
	case "y":
		run = true
	case "n":
		run = false
	case "esc", "q":
		m.cancelStart(s.startReq)
		return nil
	default:
		return nil
	}
	r := s.startReq
	r.args.Setup = api.SetupSkip
	if run {
		r.args.Setup = api.SetupRun
	}
	be, host, repo := m.be, s.host, s.q.Repo
	remember := func() tea.Msg {
		if err := be.RememberSetup(host, repo, run); err != nil {
			return opDoneMsg{host: host, err: err}
		}
		return nil
	}
	return tea.Batch(remember, m.startAgain(r))
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
