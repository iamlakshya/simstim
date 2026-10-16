package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// folderAsk asks whether a new session's folder may be made, when it does not
// exist on the host. A yes starts the session again, and the host makes it.
type folderAsk struct {
	host   string
	q      client.FolderQuestion
	args   client.NewArgs
	attach bool
	// back is the new-session form that started this, if any; it comes back
	// while the start runs again, or when the user cancels.
	back *newSession
}

// askFolder opens the question, in place of the form that started it.
func (m *Model) askFolder(msg startedMsg, q client.FolderQuestion) tea.Cmd {
	back, _ := m.modal.(*newSession)
	m.modal = &folderAsk{host: msg.host, q: q, args: msg.args, attach: msg.attach, back: back}
	return nil
}

func (f *folderAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y":
	case "n", "esc", "q":
		m.cancelStart(f.back)
		return nil
	default:
		return nil
	}
	h := m.answered(f.host, f.back)
	if h == nil {
		return nil
	}
	a := f.args
	a.MakeFolder = true
	return m.startSession(h, a, f.attach)
}

func (f *folderAsk) view(m *Model) string {
	w := m.dialogWidth()
	folder := f.q.Folder
	if h := m.hostByName(f.host); h != nil {
		folder = homePath(h, folder)
	}
	body := make([]string, 1, 8) // a blank line first
	body = append(body, wrap("The folder "+folder+" does not exist on "+f.host+".", w-4)...)
	body = append(body, "")
	body = append(body, wrap("Make it, with any missing folders above it, and start the session there?", w-4)...)
	return sheet(w, "Make the folder?", "", []hint{{"y", "make it"}, {"n", "cancel"}}, body, toneNormal)
}
