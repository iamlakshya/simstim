//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

func outerModel(pm tea.Model) *Model {
	switch m := pm.(*framed).m.(type) {
	case *Model:
		return m
	case *resumedExecModel:
		return m.m
	default:
		panic("unexpected outer model")
	}
}

func outerAction(m *Model, action string) tea.Cmd {
	switch action {
	case "editor":
		return m.openSettings()
	case "shell":
		return m.openShell(m.hostByName("box"))
	case "keygen":
		g := &guide{step: gdRentKey}
		m.modal = g
		return g.keyKey(m, "k")
	default:
		panic("unknown outer action")
	}
}

type interactiveActionReadyMsg struct{}

type outerCommandModel struct {
	tea.Model
	model                             *Model
	action                            string
	started                           <-chan struct{}
	t                                 *testing.T
	begun                             bool
	accepted                          *pendingExec
	reservations, quits, tierResolved int
}

func (m *outerCommandModel) Init() tea.Cmd {
	first := func() tea.Msg { <-m.started; return interactiveActionReadyMsg{} }
	return tea.Batch(m.Model.Init(), first)
}

func (m *outerCommandModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if _, ok := msg.(interactiveActionReadyMsg); ok {
		if m.begun {
			m.t.Error("interactive action started twice")
		}
		m.begun = true
		cmd = outerAction(m.model, m.action)
	} else {
		if _, ok := msg.(hostTierResolvedMsg); ok {
			m.tierResolved++
		}
		_, cmd = m.Model.Update(msg)
	}
	if pe := m.model.pendingExec; pe != nil {
		if m.accepted == nil {
			m.accepted = pe
			m.reservations++
		} else if pe != m.accepted {
			m.t.Error("interactive reservation changed before handoff")
		}
	}
	return m, cmd
}

func (m *outerCommandModel) observeQuit(_ tea.Model, msg tea.Msg) tea.Msg {
	if _, ok := msg.(tea.QuitMsg); ok {
		m.quits++
		if m.accepted == nil || m.model.pendingExec != m.accepted || m.reservations != 1 || m.model.pendingAttach != nil || m.model.jacking != nil {
			m.t.Error("interactive quit did not retain its one actual outer reservation")
		}
	}
	return msg
}

func TestInteractiveCommandsUseOuterTerminalHandoff(t *testing.T) {
	for _, action := range []string{"editor", "shell", "keygen"} {
		for _, commandFails := range []bool{false, true} {
			t.Run(action+"/"+map[bool]string{false: "success", true: "command-fails"}[commandFails], func(t *testing.T) {
				h := hosttest.New(t)
				terminal := hosttest.NewTerminal(t, 90, 30)
				marker := filepath.Join(t.TempDir(), "stdio.txt")
				t.Setenv("TEST_EXEC_RECORD", marker)
				script := "test -t 0 && test -t 1 || exit 81\nprintf 'stdio-ok\\n' > \"$TEST_EXEC_RECORD\"\n"
				if commandFails {
					script += "exit 7\n"
				}
				h.Script("fake-editor", script)
				h.Script("ssh", execSSHFixture(t, script))
				h.Script("ssh-keygen", script)
				t.Setenv("EDITOR", filepath.Join(h.Root, "bin", "fake-editor"))
				t.Setenv("VISUAL", "")
				t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
				before, err := term.GetState(int(terminal.TTY.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				first, next := newDemoHeldReader(true), newDemoHeldReader(true)
				var opens atomic.Int32
				restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
					if _, err := term.MakeRaw(int(terminal.TTY.Fd())); err != nil {
						return nil, err
					}
					if opens.Add(1) == 1 {
						return &execCancellableReader{first}, nil
					}
					return &execCancellableReader{next}, nil
				})
				oldErr, oldProgram := os.Stderr, runProgram
				os.Stderr = terminal.TTY
				restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
				t.Cleanup(func() {
					first.unblock()
					next.unblock()
					restoreReader()
					restoreFiles()
					os.Stderr, runProgram = oldErr, oldProgram
				})
				runs := 0
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				runProgram = func(_ context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
					runs++
					m := outerModel(pm)
					if runs == 1 {
						driver := &outerCommandModel{Model: pm, model: m, action: action, started: first.entered, t: t}
						_, err := tea.NewProgram(driver, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler(), tea.WithFilter(driver.observeQuit)).Run()
						if !driver.begun || driver.reservations != 1 || driver.quits != 1 {
							t.Errorf("interactive handoff: begun=%v reservations=%d quits=%d", driver.begun, driver.reservations, driver.quits)
						}
						if action == "shell" && driver.tierResolved != 1 {
							t.Errorf("shell processed %d tier results, want one", driver.tierResolved)
						}
						first.mu.Lock()
						reading, closes := first.reading, first.closes
						first.mu.Unlock()
						if reading || closes != 1 {
							t.Errorf("program returned before its owned reader stopped: reading=%v closes=%d", reading, closes)
						}
						return err
					}
					if runs != 2 {
						t.Fatalf("unexpected program %d", runs)
					}
					if _, ok := pm.(*framed).m.(*resumedExecModel); !ok {
						t.Fatalf("resume wraps %T", pm.(*framed).m)
					}
					// Init applies the command's result. It must not invalidate a live
					// Guide's key-generation scope before delivering that result.
					_, err := tea.NewProgram(execInitialModel{Model: pm, first: func() tea.Msg { <-next.entered; return tea.QuitMsg{} }}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()

					return err
				}
				if err := runWith(ctx, execOwnedBackend(t), Options{}); err != nil {
					t.Fatalf("ordinary command result ended launcher: %v", err)
				}
				if ctx.Err() != nil || runs != 2 || opens.Load() != 2 {
					t.Fatalf("runs=%d opens=%d context=%v", runs, opens.Load(), ctx.Err())
				}
				data, err := os.ReadFile(marker)
				if err != nil || string(data) != "stdio-ok\n" {
					t.Fatalf("real terminal stdio=%q,%v", data, err)
				}
				first.mu.Lock()
				reading, closedEarly, closes := first.reading, first.closedDuringRead, first.closes
				first.mu.Unlock()
				if reading || closedEarly || closes != 1 {
					t.Fatalf("first reader reading=%v close-under-read=%v closes=%d", reading, closedEarly, closes)
				}
				after, err := term.GetState(int(terminal.TTY.Fd()))
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("terminal state changed: %v", err)
				}
			})
		}
	}
}

func TestOuterExecDoesNotStartChildWhenLauncherReaderIsStuck(t *testing.T) {
	terminal := hosttest.NewTerminal(t, 90, 30)
	before, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	reader := newDemoHeldReader(false)
	restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) { return reader, nil })
	oldErr, oldProgram := os.Stderr, runProgram
	os.Stderr = terminal.TTY
	restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
	t.Cleanup(func() {
		reader.unblock()
		_ = keyread.Release(terminal.TTY)
		restoreReader()
		restoreFiles()
		os.Stderr, runProgram = oldErr, oldProgram
	})
	var started atomic.Bool
	runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
		m := outerModel(pm)
		cmd := m.execWithInput(&funcExec{run: func(*os.File, *os.File) error { started.Store(true); return nil }}, func(error) tea.Msg { return nil })
		_, err := tea.NewProgram(execInitialModel{Model: pm, first: func() tea.Msg { <-reader.entered; return cmd() }}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = runWith(ctx, newFakeBackend(), Options{})
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "terminal_busy" || started.Load() {
		t.Fatalf("result=%v child started=%v", err, started.Load())
	}
	after, err := term.GetState(int(terminal.TTY.Fd()))
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("terminal state changed: %v", err)
	}
	reader.unblock()
	<-reader.left
}

type suspendObservedModel struct {
	tea.Model
	keys chan string
}

func (m suspendObservedModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.Model.Update(msg)
	if k, ok := msg.(tea.KeyPressMsg); ok {
		m.keys <- k.String()
	}
	return m, cmd
}

// The application has no suspend binding. Prove this with a real input parser
// and event loop, trapping a hypothetical SuspendMsg before it could stop the
// test process group. A later q must still use the same program and reader.
func TestLauncherCtrlZDoesNotInvokeBubbleTeaSuspend(t *testing.T) {
	terminal := hosttest.NewTerminal(t, 90, 30)
	reader := newDemoHeldReader(true)
	restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) { return &execCancellableReader{reader}, nil })
	oldErr, oldProgram := os.Stderr, runProgram
	os.Stderr = terminal.TTY
	restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
	t.Cleanup(func() { reader.unblock(); restoreReader(); restoreFiles(); os.Stderr, runProgram = oldErr, oldProgram })
	keys := make(chan string, 4)
	var suspends atomic.Int32
	runs := 0
	runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
		runs++
		_, err := tea.NewProgram(suspendObservedModel{Model: pm, keys: keys}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			if _, ok := msg.(tea.SuspendMsg); ok {
				suspends.Add(1)
				return tea.QuitMsg{}
			}
			return msg
		})).Run()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	typed := make(chan struct{})
	go func() {
		defer close(typed)
		<-reader.entered
		select {
		case reader.input <- []byte{0x1a}:
		case <-ctx.Done():
			return
		}
		select {
		case key := <-keys:
			if key != "ctrl+z" {
				return
			}
		case <-ctx.Done():
			return
		}
		select {
		case reader.input <- []byte("q"):
		case <-ctx.Done():
			return
		}
	}()
	err := runWith(ctx, newFakeBackend(), Options{})
	<-typed
	if err != nil || ctx.Err() != nil || runs != 1 || suspends.Load() != 0 {
		t.Fatalf("result=%v context=%v runs=%d suspends=%d", err, ctx.Err(), runs, suspends.Load())
	}
	select {
	case key := <-keys:
		if key != "q" {
			t.Fatalf("later key=%q", key)
		}
	default:
		t.Fatal("later q did not reach the same program")
	}
}
