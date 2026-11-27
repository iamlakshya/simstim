package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type grantQuestion struct {
	request  grantQuestionRequest
	previous modal
	name     input
	wrong    bool
}

func (q *grantQuestion) answer(yes bool) {
	select {
	case q.request.answer <- yes:
	default:
	}
}

func (q *grantQuestion) paste(text string) {
	if q.request.ctx.Err() == nil && q.request.question.Kind == client.ConfirmHostName {
		q.name.insert(text)
	}
}

func (q *grantQuestion) key(m *Model, key tea.KeyPressMsg) tea.Cmd {
	if m.modal != q || q.request.ctx.Err() != nil || m.ctx.Err() != nil {
		return nil
	}
	yes := false
	switch key.String() {
	case "esc", "ctrl+c":
	case "enter":
		if q.request.question.Kind == client.ConfirmHostName {
			if q.name.value() != q.request.question.Host {
				q.wrong = true
				return nil
			}
			yes = true
		}
	default:
		if q.request.question.Kind == client.ConfirmHostName {
			q.name.key(key)
			return nil
		}
		switch key.String() {
		case "y":
			yes = true
		case "n", "q":
		default:
			return nil
		}
	}
	q.answer(yes)
	m.modal = q.previous
	return nil
}

func (q *grantQuestion) view(m *Model) string {
	width := m.dialogWidth()
	body := []string{""}
	body = append(body, wrap(client.CleanLine(q.request.question.Prompt), width-4)...)
	body = append(body, "")
	keys := []hint{{"y", "approve once"}, {"n/enter", "cancel"}, {"esc", "cancel"}}
	if q.request.question.Kind == client.ConfirmHostName {
		body = append(body, "Type the saved host name exactly:", q.name.view(width-4, true))
		keys = []hint{{"enter", "check the name"}, {"esc", "cancel"}}
		if q.wrong {
			body = append(body, "The name does not match.")
		}
	}
	return sheet(width, "Approve this grant?", "", keys, body, toneNormal)
}

// questionOwner unwraps temporary live questions without changing ownership
// of the modal operation underneath them.
func questionOwner(active modal) modal {
	for {
		switch question := active.(type) {
		case *hostTierQuestion:
			active = question.previous
		case *grantQuestion:
			active = question.previous
		default:
			return active
		}
	}
}
