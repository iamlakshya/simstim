//go:build !windows

package tui

// Adapted from the exact recovered review WriteFile331 source. The immutable
// original and its SHA remain in round16/probe15-partial/recovered-exact.
// Only the command driver accepts nil after the correct refusal of a second
// handoff. Quit, attach count, program count and selected-session assertions
// retain their meaning. Accepted-parent positive fixture IDs use legal
// base32 values (first s2, second s3). This is not an unmodified replay.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

type recoveredHandoffAttachRecordingBackend struct {
	*fakeBackend
	attaches atomic.Int32
}

func (b *recoveredHandoffAttachRecordingBackend) Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error) {
	b.attaches.Add(1)
	return client.AttachResult{Detached: true}, nil
}

type recoveredHandoffDualHandoffModel struct {
	tea.Model
	m       *Model
	started <-chan struct{}
}

func (p recoveredHandoffDualHandoffModel) Init() tea.Cmd {
	execCmd := p.m.openSettings()
	attachCmd := p.m.attachSelected()
	return tea.Batch(
		p.Model.Init(),
		func() tea.Msg {
			<-p.started
			if execCmd == nil {
				return nil
			}
			return execCmd()
		},
		func() tea.Msg {
			<-p.started
			if attachCmd == nil {
				return nil
			}
			return attachCmd()
		},
	)
}

func TestRecoveredHandoffWindowPreservesQuitAndJackSelection(t *testing.T) {
	t.Run("pending-exec-plus-attach-hijacks-q", func(t *testing.T) {
		h := hosttest.New(t)
		terminal := hosttest.NewTerminal(t, 90, 30)
		h.Script("fake-editor", "exit 0\n")
		t.Setenv("EDITOR", filepath.Join(h.Root, "bin", "fake-editor"))
		t.Setenv("VISUAL", "")

		fb := newFakeBackend()
		fb.hosts = []client.Host{{Name: "box", SSH: "box"}}
		s := client.Session{ID: "s2", Name: "job", Agent: "shell", State: "idle", Alive: true}
		fb.state["box"] = &fakeHost{sessions: []client.Session{s}}
		be := &recoveredHandoffAttachRecordingBackend{fakeBackend: fb}

		first, next, third := newDemoHeldReader(true), newDemoHeldReader(true), newDemoHeldReader(true)
		var opens atomic.Int32
		restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
			switch opens.Add(1) {
			case 1:
				return &execCancellableReader{first}, nil
			case 2:
				return &execCancellableReader{next}, nil
			default:
				return &execCancellableReader{third}, nil
			}
		})
		oldErr, oldProgram := os.Stderr, runProgram
		os.Stderr = terminal.TTY
		restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
		t.Cleanup(func() {
			first.unblock()
			next.unblock()
			third.unblock()
			restoreReader()
			restoreFiles()
			os.Stderr, runProgram = oldErr, oldProgram
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		runs := 0
		runProgram = func(_ context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
			runs++
			switch runs {
			case 1:
				m := outerModel(pm)
				hc := m.hostByName("box")
				hc.sessions, hc.loaded, hc.status = []client.Session{s}, true, stOnline
				m.screen, m.cur, m.sel, m.autoSel = scrSessions, "box", sessKey{"box", "s2"}, false
				m.syncSel()
				_, err := tea.NewProgram(recoveredHandoffDualHandoffModel{Model: pm, m: m, started: first.entered},
					tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			case 2:
				go func() {
					select {
					case <-next.entered:
						next.input <- []byte("q")
					case <-ctx.Done():
					}
				}()
				_, err := tea.NewProgram(pm, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			default:
				go func() {
					select {
					case <-third.entered:
						third.input <- []byte("q")
					case <-ctx.Done():
					}
				}()
				_, err := tea.NewProgram(pm, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			}
		}

		if err := runWith(ctx, be, Options{}); err != nil {
			t.Fatalf("runWith: %v", err)
		}
		if be.attaches.Load() != 0 || runs != 2 {
			t.Fatalf("pressing 'q' after editor handoff was hijacked by stale pendingAttach: attaches=%d programRuns=%d (want attaches=0 runs=2)",
				be.attaches.Load(), runs)
		}
	})

	t.Run("mouse-click-during-jack", func(t *testing.T) {
		fb := newFakeBackend()
		fb.hosts = []client.Host{{Name: "box", SSH: "box"}}
		s1 := client.Session{ID: "s2", Name: "first", Agent: "shell", State: "idle", Alive: true}
		s2 := client.Session{ID: "s3", Name: "second", Agent: "shell", State: "idle", Alive: true}
		fb.state["box"] = &fakeHost{sessions: []client.Session{s1, s2}}
		hs := newHarness(t, fb, 90, 30)
		hs.m.motion = true
		hc := hs.m.hostByName("box")
		hc.sessions, hc.loaded, hc.status = []client.Session{s1, s2}, true, stOnline
		hs.m.openHost(hc)
		_ = hs.m.View()

		_, _ = hs.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if hs.m.jacking == nil || hs.m.jacking.id != "s2" {
			t.Fatalf("expected jacking into s2, got %+v", hs.m.jacking)
		}
		clickS2 := tea.MouseClickMsg(tea.Mouse{X: hs.m.geo.x + 2, Y: hs.m.geo.listY + 1, Button: tea.MouseLeft})
		_, _ = hs.m.Update(clickS2)
		_, _ = hs.m.Update(clickS2)
		if hs.m.sel.id != "s2" || hs.m.jacking.id != "s2" {
			t.Fatalf("mouse click bypassed m.jacking guard: sel=%v jacking.id=%s (want s2)",
				hs.m.sel, hs.m.jacking.id)
		}
	})
}
