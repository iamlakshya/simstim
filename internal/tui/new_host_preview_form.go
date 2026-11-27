package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/client"
)

// A rule list and Auto have no selected connection. All agent kinds remain
// editable; actual installation and admission are checked on the real attempt.
func neutralAgentChoices() []agentChoice {
	choices := make([]agentChoice, 0, len(agents.Table)+1)
	for _, agent := range agents.Table {
		choices = append(choices, agentChoice{kind: agent.Kind, installed: true})
	}
	return append(choices, agentChoice{kind: otherAgent, installed: true})
}

func newNeutralSessionDialog(preview hostSelectionPreview) *newSession {
	d := &newSession{
		preview: preview, choices: neutralAgentChoices(),
		command: newInput("", "a command, for example aider --model sonnet"),
		folder:  newInput("~", "~"), name: newInput("", "the folder name"),
	}
	for index, choice := range d.choices {
		if choice.kind == "shell" {
			d.ai = index
		}
	}
	return d
}

func (d *newSession) useNeutralHost(preview hostSelectionPreview, selector string) {
	d.stopFolders()
	currentAgent := d.agent()
	d.host, d.sourceHost, d.selector = "", client.Host{}, selector
	d.preview, d.choices, d.finding, d.ai = preview, neutralAgentChoices(), false, 0
	for index, choice := range d.choices {
		if choice.kind == currentAgent {
			d.ai = index
			break
		}
	}
	d.suggest, d.si, d.hints, d.choosingHost = nil, 0, false, false
	d.downgrade, d.err = false, nil
}

func (d *newSession) selectionName() string {
	if d.preview.automatic {
		return "Auto"
	}
	if d.preview.neutral() {
		return strings.Join(shownAll(d.preview.names), " → ")
	}
	return shown(d.host)
}

func (d *newSession) selectionTitle() string {
	if d.preview.neutral() {
		return "New session"
	}
	return "New session on " + shown(d.host)
}

func (d *newSession) startingText() string {
	if d.preview.neutral() {
		return "Choosing a host for " + shown(d.agent()) + "…"
	}
	return fmt.Sprintf("Starting %s on %s…", shown(d.agent()), shown(d.host))
}

// Start prepares a fresh concrete selection and attempts. The preview supplies
// only the display metadata that must still match the submitted form.
func (m *Model) submitPreviewedNewHost(msg newHostResolvedMsg) tea.Cmd {
	d := msg.form
	if d == nil || m.modal != d || !sameHostPreview(d.preview, msg.preview) ||
		!msg.preview.neutral() && (d.host != msg.host.Name || !client.SameEntry(d.sourceHost, msg.host)) {
		return m.newHostSelectionError(d, &client.UserError{Code: "host_changed", Kind: client.KindRefused,
			Msg: "the form's host selection changed before the start", Hint: "Close this form, then choose the host again."})
	}
	d.starting = true
	return m.startSelectedNewSession(msg.selector, msg.preview, startReq{args: msg.args, attach: msg.attach, form: d})
}

func (m *Model) acceptNeutralHostPreview(msg newHostResolvedMsg) tea.Cmd {
	switch msg.action {
	case newHostQuick, newHostForm:
		m.modal = newNeutralSessionDialog(msg.preview)
	case newHostChoice:
		if msg.form != nil && m.modal == msg.form {
			msg.form.useNeutralHost(msg.preview, msg.selector)
		}
	}
	return nil
}
