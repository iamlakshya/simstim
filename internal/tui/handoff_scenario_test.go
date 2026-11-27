package tui

import (
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

type handoffScenarioStepMsg struct{}

// handoffScenarioModel drives the same owner as a real program. The test's
// program callback supplies its context to Bubble Tea, not to Model internals.
type handoffScenarioModel struct {
	tea.Model
	t        *testing.T
	phase    int
	exec     bool
	children *int
}

func (m *handoffScenarioModel) Init() tea.Cmd {
	return func() tea.Msg { return handoffScenarioStepMsg{} }
}

func (m *handoffScenarioModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(handoffScenarioStepMsg); ok {
		if m.exec {
			return m, m.stepExec()
		}
		return m, m.stepAttach()
	}
	_, cmd := m.Model.Update(msg)
	return m, cmd
}

func (m *handoffScenarioModel) checkedQuit(cmd tea.Cmd, missing string) tea.Cmd {
	if cmd == nil {
		m.t.Fatal(missing)
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		m.t.Fatal("q did not produce quit")
	}
	return func() tea.Msg { return msg }
}

func (m *handoffScenarioModel) stepExec() tea.Cmd {
	fr := m.Model.(*framed)
	switch m.phase {
	case 1:
		owner := fr.m.(*Model)
		x := &funcExec{run: func(*os.File, *os.File) error { (*m.children)++; return nil }}
		quit := owner.execWithInput(x, func(error) tea.Msg { return editorDoneMsg{} })
		_ = owner.attachTo(&hostConn{host: client.Host{Name: "box", SSH: "box"}}, "late", "late")
		if quit == nil {
			m.t.Fatal("exec did not request exit")
		}
		return quit
	case 2:
		resumed, ok := fr.m.(*resumedExecModel)
		if !ok {
			m.t.Fatalf("second program=%T", fr.m)
		}
		_ = resumed.Init()
		if resumed.m.pendingAttach != nil || resumed.m.pendingExec != nil {
			m.t.Fatal("resume retained another transfer")
		}
		return m.checkedQuit(resumed.m.onKey(keyForRune('q')), "q did not quit")
	default:
		m.t.Fatalf("stale attach started a third program: %d", m.phase)
		return nil
	}
}

func (m *handoffScenarioModel) stepAttach() tea.Cmd {
	fr := m.Model.(*framed)
	switch m.phase {
	case 1:
		owner := fr.m.(*Model)
		cmd := owner.attachTo(owner.hostByName("box"), "s2", "session")
		if cmd == nil {
			m.t.Fatal("attach did not request exit")
		}
		return cmd
	case 2:
		resumed, ok := fr.m.(*resumedModel)
		if !ok {
			m.t.Fatalf("resume wraps %T", fr.m)
		}
		_ = resumed.Init()
		if resumed.m.handingOff() {
			m.t.Fatal("clean attach resume retained its transfer reservation")
		}
		return m.checkedQuit(resumed.m.onKey(keyForRune('q')), "q was blocked after clean attach")
	default:
		m.t.Fatalf("q was hijacked by a third program: %d", m.phase)
		return nil
	}
}
