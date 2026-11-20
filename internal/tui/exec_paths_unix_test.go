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
)

type execPathModel struct {
	tea.Model
	action      string
	readStarted <-chan struct{}
	ordinary    *atomic.Int32
}

func (m *execPathModel) Init() tea.Cmd {
	model := m.Model.(*framed).m.(*Model)
	model.handoff = false
	h := &hostConn{host: client.Host{Name: "box", SSH: "box"}}
	var command tea.Cmd
	switch m.action {
	case "editor":
		command = model.openSettings()
	case "shell":
		command = model.openShell(h)
	case "keygen":
		command = (&guide{step: gdRentKey}).keyKey(model, "k")
	case "attach":
		command = model.attachTo(h, "s1", "session")
	}
	first := func() tea.Msg { <-m.readStarted; return command() }
	return tea.Batch(m.Model.Init(), first)
}

func (m *execPathModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.Model.Update(msg)
	switch msg.(type) {
	case editorDoneMsg, shellDoneMsg, rentKeyMsg, attachDoneMsg:
		// Make a wrong ordinary callback fail promptly, rather than waiting
		// for the otherwise unresponsive terminal to reach the test deadline.
		m.ordinary.Add(1)
		cmd = tea.Batch(cmd, tea.Quit)
	}
	return m, cmd
}

func TestExecEntryPointsStopWhenInputCannotResume(t *testing.T) {
	for _, action := range []string{"editor", "shell", "keygen", "attach"} {
		t.Run(action, func(t *testing.T) {
			h := hosttest.New(t)
			terminal := hosttest.NewTerminal(t, 90, 30)
			marker := filepath.Join(t.TempDir(), "stdio.txt")
			t.Setenv("TEST_EXEC_RECORD", marker)
			script := `test -t 0 && test -t 1 || exit 81
printf 'stdio-ok\n' > "$TEST_EXEC_RECORD"
`
			h.Script("fake-editor", script)
			h.Script("ssh", script)
			h.Script("ssh-keygen", script)
			t.Setenv("EDITOR", filepath.Join(h.Root, "bin", "fake-editor"))
			t.Setenv("VISUAL", "")
			t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
			oldErr, oldProgram := os.Stderr, runProgram
			os.Stderr = terminal.TTY
			restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
			reader := newDemoHeldReader(true)
			var opens, ordinary atomic.Int32
			resumeFailure := errors.New("entry point reader restart failed")
			restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
				if opens.Add(1) == 1 {
					return &execCancellableReader{reader}, nil
				}
				return nil, resumeFailure
			})
			t.Cleanup(func() {
				reader.unblock()
				restoreReader()
				restoreFiles()
				os.Stderr, runProgram = oldErr, oldProgram
			})
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			be := newFakeBackend()
			runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
				model := &execPathModel{Model: pm, action: action, readStarted: reader.entered, ordinary: &ordinary}
				_, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = runWith(ctx, be, Options{})
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Code != "terminal_failed" || !errors.Is(err, resumeFailure) || ordinary.Load() != 0 {
				t.Fatalf("%s: runWith=%v (ue=%+v), ordinary callbacks=%d", action, err, ue, ordinary.Load())
			}
			if action == "attach" {
				if len(be.attached) != 1 || be.attached[0].In != terminal.TTY || be.attached[0].Out != terminal.TTY {
					t.Fatalf("attach lost real terminal files: %+v", be.attached)
				}
			} else if data, err := os.ReadFile(marker); err != nil || string(data) != "stdio-ok\n" {
				t.Fatalf("%s did not receive real terminal input/output: %q, %v", action, data, err)
			}
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("%s left terminal modes changed: %v", action, err)
			}
		})
	}
}
