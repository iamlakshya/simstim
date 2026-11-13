package tui

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func noopHandoffExec() *funcExec {
	return &funcExec{run: func(*os.File, *os.File) error { return nil }}
}

func TestFirstHandoffIntentWinsBeforeInputStopCompletes(t *testing.T) {
	for _, first := range []string{"exec", "attach"} {
		t.Run(first, func(t *testing.T) {
			hs := sessions(t, 80, 24)
			m := hs.m
			m.handoff = true
			h, s := m.selectedPair()
			if h == nil || s == nil {
				t.Fatal("fixture has no session")
			}
			x := noopHandoffExec()
			if first == "exec" {
				if m.execWithInput(x, func(error) tea.Msg { return nil }) == nil {
					t.Fatal("first exec was not queued")
				}
				before := m.mem.Last
				if cmd := m.attachTo(h, "late", "late"); cmd != nil {
					t.Fatal("late attach was queued")
				}
				if m.pendingExec == nil || m.pendingExec.command != x || m.pendingAttach != nil || m.mem.Last != before {
					t.Fatal("late attach changed the accepted exec intent or memory")
				}
				if cmd := m.execWithInput(noopHandoffExec(), func(error) tea.Msg { return nil }); cmd != nil || m.pendingExec.command != x {
					t.Fatal("later exec replaced the first")
				}
			} else {
				if m.attachTo(h, s.ID, s.Name) == nil {
					t.Fatal("first attach was not queued")
				}
				accepted := m.pendingAttach
				if cmd := m.execWithInput(x, func(error) tea.Msg { return nil }); cmd != nil {
					t.Fatal("late exec was queued")
				}
				if m.pendingExec != nil || m.pendingAttach != accepted {
					t.Fatal("late exec replaced the accepted attach")
				}
				if cmd := m.attachTo(h, "late", "late"); cmd != nil || m.pendingAttach != accepted {
					t.Fatal("later attach replaced the first")
				}
			}
		})
	}
}

func TestTransferWindowIgnoresAllUserInput(t *testing.T) {
	for _, state := range []string{"exec", "attach", "jack"} {
		for _, action := range []string{"key-quit", "key-cancel", "key-open", "click", "wheel", "paste"} {
			t.Run(state+"/"+action, func(t *testing.T) {
				hs := sessions(t, 80, 24)
				m := hs.m
				h, s := m.selectedPair()
				m.motion = true
				switch state {
				case "exec":
					m.pendingExec = &pendingExec{command: noopHandoffExec()}
				case "attach":
					m.pendingAttach = &pendingAttach{host: h.name(), id: s.ID}
				case "jack":
					m.jacking = &jackIn{h: h, id: s.ID, at: m.now()}
				}
				_ = m.View()
				selection, hostSelection, jack := m.sel, m.hostSel, m.jacking
				var cmd tea.Cmd
				switch action {
				case "key-quit":
					cmd = m.onKey(keyForRune('q'))
				case "key-cancel":
					cmd = m.onKey(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				case "key-open":
					cmd = m.onKey(keyForRune('2'))
				case "click":
					cmd = m.onClick(otherTransferRowClick(t, m))
				case "wheel":
					cmd = m.onWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				case "paste":
					w := newWizard()
					m.modal = w
					before := w.in[0].value()
					cmd = m.onPaste("ignored")
					if w.in[0].value() != before {
						t.Fatal("paste changed a modal during transfer")
					}
				}
				if cmd != nil || m.sel != selection || m.hostSel != hostSelection || m.jacking != jack || m.quitArmed {
					t.Fatalf("%s changed state during %s transfer", action, state)
				}
			})
		}
	}
}

func otherTransferRowClick(t *testing.T, m *Model) tea.MouseClickMsg {
	t.Helper()
	for i, row := range m.rows() {
		if row.kind == rowSession && row.key() != m.sel && i >= m.geo.top && i < m.geo.top+m.geo.listN {
			return tea.MouseClickMsg{X: m.geo.x + 2, Y: m.geo.listY + i - m.geo.top, Button: tea.MouseLeft}
		}
	}
	t.Fatal("fixture has no second visible session")
	return tea.MouseClickMsg{}
}

func keyForRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func TestJackReservationRejectsOtherTransferIntents(t *testing.T) {
	hs := sessions(t, 80, 24)
	m := hs.m
	m.motion = true
	m.handoff = true
	h, s := m.selectedPair()
	if m.jack(h, s.ID, s.Name) == nil {
		t.Fatal("first jack was not started")
	}
	first := m.jacking
	if m.jack(h, "other", "other") != nil || m.jacking != first {
		t.Fatal("late jack replaced the first")
	}
	if m.attachTo(h, "other", "other") != nil || m.execWithInput(noopHandoffExec(), func(error) tea.Msg { return nil }) != nil {
		t.Fatal("another transfer bypassed the jack reservation")
	}
	if m.pendingExec != nil || m.pendingAttach != nil {
		t.Fatal("another transfer intent was recorded")
	}
	if m.onJack() == nil || m.pendingAttach == nil || m.pendingAttach.id != s.ID || m.jacking != nil {
		t.Fatal("accepted jack did not become its own attach")
	}
}

func TestResumeRetiresEveryTransferIntentBeforeCallbacks(t *testing.T) {
	hs := sessions(t, 80, 24)
	m := hs.m
	m.pendingExec = &pendingExec{command: noopHandoffExec()}
	m.pendingAttach = &pendingAttach{id: "stale"}
	m.jacking = &jackIn{}
	m.quitArmed = true
	called := false
	_ = m.resumeAfterHandoff(func() tea.Cmd {
		called = true
		if m.transferInputBlocked() || m.quitArmed {
			t.Fatal("resume callback saw stale transfer state")
		}
		return nil
	})
	if !called || m.pendingExec != nil || m.pendingAttach != nil || m.jacking != nil || m.quitArmed {
		t.Fatal("resume retained a stale transfer intent")
	}
}

// Reproduce the outer loop's exact editor -> resume -> q sequence. A result
// arriving while the input-stop command waits must not turn q into an attach.
func TestLateAttachCannotHijackQuitAfterExecHandoff(t *testing.T) {
	be := newFakeBackend()
	runs, children := 0, 0
	withFakeLauncherIO(t, func(*os.File) bool { return true }, func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
		runs++
		fr := pm.(*framed)
		if runs == 1 {
			m := fr.m.(*Model)
			x := &funcExec{run: func(*os.File, *os.File) error { children++; return nil }}
			quit := m.execWithInput(x, func(error) tea.Msg { return editorDoneMsg{} })
			_ = m.attachTo(&hostConn{host: client.Host{Name: "box", SSH: "box"}}, "late", "late")
			if quit == nil {
				t.Fatal("exec did not request exit")
			}
			_ = quit()
		} else if runs == 2 {
			resumed, ok := fr.m.(*resumedExecModel)
			if !ok {
				t.Fatalf("second program=%T", fr.m)
			}
			_ = resumed.Init()
			if resumed.m.pendingAttach != nil || resumed.m.pendingExec != nil {
				t.Fatal("resume retained another transfer")
			}
			cmd := resumed.m.onKey(keyForRune('q'))
			if cmd == nil {
				t.Fatal("q did not quit")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("q did not produce quit")
			}
		} else {
			t.Fatalf("stale attach started a third program: %d", runs)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWith(ctx, be, Options{}); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || children != 1 || len(be.attached) != 0 {
		t.Fatalf("runs=%d children=%d attaches=%d", runs, children, len(be.attached))
	}
}

func TestCleanAttachResumeAllowsOrdinaryQuit(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	be.state["box"] = &fakeHost{}
	runs := 0
	withFakeLauncherIO(t, func(*os.File) bool { return true }, func(_ context.Context, pm tea.Model, _ io.Reader, _ io.Writer) error {
		runs++
		fr := pm.(*framed)
		switch runs {
		case 1:
			m := fr.m.(*Model)
			cmd := m.attachTo(m.hostByName("box"), "s2", "session")
			if cmd == nil {
				t.Fatal("attach did not request exit")
			}
			_ = cmd()
		case 2:
			resumed, ok := fr.m.(*resumedModel)
			if !ok {
				t.Fatalf("resume wraps %T", fr.m)
			}
			_ = resumed.Init()
			if resumed.m.handingOff() {
				t.Fatal("clean attach resume retained its transfer reservation")
			}
			cmd := resumed.m.onKey(keyForRune('q'))
			if cmd == nil {
				t.Fatal("q was blocked after clean attach")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("q did not produce quit")
			}
		default:
			t.Fatalf("q was hijacked by a third program: %d", runs)
		}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runWith(ctx, be, Options{}); err != nil {
		t.Fatal(err)
	}
	if runs != 2 || len(be.attached) != 1 {
		t.Fatalf("runs=%d attaches=%d", runs, len(be.attached))
	}
}
