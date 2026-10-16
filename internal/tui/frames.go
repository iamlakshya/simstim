package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
)

// framed times each frame of the launcher while the client's debug has the
// launcher category on: Bubble Tea calls Update for a message, then View.
// Each frame is a record with the time in each and the Go type of the
// message, never its contents.
type framed struct {
	m      tea.Model
	f      *diag.File
	msg    string
	update int64
}

// timeFrames wraps m when the launcher category is on.
func timeFrames(m tea.Model) tea.Model {
	f := client.DebugFile()
	if f == nil || !client.DebugOn(diag.CatLauncher) {
		return m
	}
	return &framed{m: m, f: f}
}

func (fr *framed) Init() tea.Cmd { return fr.m.Init() }

func (fr *framed) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	start := diag.Now()
	m, cmd := fr.m.Update(msg)
	fr.m = m
	fr.update = diag.Now() - start
	fr.msg = fmt.Sprintf("%T", msg)
	return fr, cmd
}

func (fr *framed) View() tea.View {
	start := diag.Now()
	v := fr.m.View()
	end := diag.Now()
	fr.f.Add(diag.Record{T: end, Cat: diag.CatLauncher, Ev: "frame", Msg: fr.msg, Update: fr.update, View: end - start})
	fr.msg, fr.update = "", 0
	return v
}
