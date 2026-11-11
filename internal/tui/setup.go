package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// setupAsk asks whether a repository's setup script may run, when a start
// meets a repository that the user has not answered for on this host, or
// whose script changed after the user's yes. The answer is kept, and the
// start runs again with it.
type setupAsk struct {
	startReq
	q client.SetupQuestion
}

// past says what the user said before about the repository.
func (s *setupAsk) past() client.SetupPast { return s.answers.Past(s.q) }

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
	// If the script changes again before the next try, the question says
	// that it changed after the user trusted it.
	r.args, r.answers = client.AnswerSetup(r.args, s.q, run), r.answers.Answered(s.q, run)
	be, host, repo, hash := m.be, s.host, s.q.Repo, s.q.Hash
	remember := func() tea.Msg {
		warn, err := be.RememberSetup(host, repo, run, hash)
		switch {
		case err != nil:
			return opDoneMsg{host: host, err: err}
		case warn != nil:
			return setupWarnMsg{warn: warn}
		}
		return nil
	}
	return tea.Batch(remember, m.startAgain(r))
}

// setupWarnMsg is a warning about the user's answers file, such as one that
// did not parse and moved aside while an answer was kept.
type setupWarnMsg struct{ warn *client.UserError }

// toastSetupWarn shows a warning about the user's answers file.
func (m *Model) toastSetupWarn(w *client.UserError) tea.Cmd {
	return m.toastNext(toastWarning, sentence(w.Msg), w.Hint)
}

func (s *setupAsk) view(m *Model) string {
	w := m.dialogWidth()
	h := m.hostByName(s.host)
	repo, script := s.q.Repo, s.q.Script
	if h != nil {
		repo, script = homePath(h, repo), homePath(h, script)
	}
	// The repository and the script come from the host. The answer is kept
	// for the repository's own path (key), and this shows that path.
	repo, script = client.ShowPath(repo), client.ShowPath(script)
	var first string
	switch s.past() {
	case client.SetupChanged:
		first = "The setup script of " + repo + " on " + s.host + " changed after you trusted it: " + script + "."
	case client.SetupUnchecked:
		first = "You trusted the setup script of " + repo + " on " + s.host + " before this version checked its content: " + script + "."
	case client.SetupNotTrusted:
		first = "The repository " + repo + " on " + s.host + " has a setup script, " + script + "."
	}
	body := make([]string, 1, 12) // a blank line first
	for _, l := range []string{
		first,
		"It runs in the session before the agent, as you, with the session's environment and secrets. Say yes only for a repository you trust.",
		"The answer is kept for this repository on " + s.host + " until the script changes.",
	} {
		body = append(body, wrap(l, w-4)...)
		body = append(body, "")
	}
	return sheet(w, "Run its setup script?", "", []hint{{"y", "run it"}, {"n", "skip it"}, {"esc", "cancel"}}, trimBlank(body), toneNormal)
}

// setupSkipAsk offers to start without the repository's scripts, when one
// of them cannot run: it leads outside the repository (setup_outside), or
// it cannot be read (setup_unreadable). The error's hint names --no-setup,
// which the launcher has no flag for: this is the launcher's way to it.
type setupSkipAsk struct {
	startReq
	err *client.UserError
}

// setupCannotRun returns the error of a start whose repository has a
// script that cannot run.
func setupCannotRun(err error) (*client.UserError, bool) {
	ue, ok := asUserError(err)
	if !ok || ue.Code != api.CodeSetupOutside && ue.Code != api.CodeSetupUnreadable {
		return nil, false
	}
	return ue, true
}

func (s *setupSkipAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y":
		r := s.startReq
		r.args.Setup = api.SetupSkip
		return m.startAgain(r)
	case "n", "esc", "q":
		m.cancelStart(s.startReq)
	}
	return nil
}

func (s *setupSkipAsk) view(m *Model) string {
	w := m.dialogWidth()
	body := make([]string, 1, 12) // a blank line first
	for _, l := range []string{
		sentence(s.err.Msg),
		"Start the session without the repository's setup scripts? Your own hooks on " + s.host + " still run.",
	} {
		body = append(body, wrap(l, w-4)...)
		body = append(body, "")
	}
	return sheet(w, "Start without its setup script?", "", []hint{{"y", "start without it"}, {"n", "cancel"}}, trimBlank(body), toneNormal)
}
