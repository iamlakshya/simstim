package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

// makeFolderAsk asks whether a new session's folder may be made, when it does
// not exist on the host. A yes starts the session again, and the host makes
// the folder.
type makeFolderAsk struct {
	startReq
	q client.FolderQuestion
}

func (f *makeFolderAsk) key(m *Model, k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "y":
		r := f.startReq
		r.args.MakeFolder = true
		return m.startAgain(r)
	case "n", "esc", "q":
		m.cancelStart(f.startReq)
	}
	return nil
}

func (f *makeFolderAsk) view(m *Model) string {
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
