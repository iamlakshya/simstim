//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"os"
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

type execInitialModel struct {
	tea.Model
	first tea.Cmd
}

func (m execInitialModel) Init() tea.Cmd { return tea.Batch(m.Model.Init(), m.first) }

// A real tea.Exec cycle must leave the program when its input cannot restart.
// Returning the command's ordinary result would leave a raw, unresponsive TUI.
func TestExecInputFailureQuitsRealProgramAndRestoresTerminal(t *testing.T) {
	for _, mode := range []string{"factory-fails", "exec-left-stuck-reader", "pause-left-stuck-reader"} {
		t.Run(mode, func(t *testing.T) {
			terminal := hosttest.NewTerminal(t, 90, 30)
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			oldErr, oldProgram := os.Stderr, runProgram
			os.Stderr = terminal.TTY
			restoreFiles := SetLauncherFiles(terminal.TTY, terminal.TTY)
			reader := newDemoHeldReader(mode != "pause-left-stuck-reader")
			// This first reader really exits when successful cancellation is
			// requested, unlike the deliberately stuck reader in the third case.
			factoryError := errors.New("test input factory failed")
			var opens, callback atomic.Int32
			restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
				if opens.Add(1) == 1 {
					return &execCancellableReader{demoHeldReader: reader}, nil
				}
				return nil, factoryError
			})
			child := newDemoHeldReader(false)
			childDone := make(chan struct{})
			t.Cleanup(func() {
				reader.unblock()
				child.unblock()
				_ = keyread.Release(terminal.TTY)
				restoreReader()
				restoreFiles()
				os.Stderr, runProgram = oldErr, oldProgram
			})
			runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
				m := pm.(*framed).m.(*Model)
				m.handoff = false // exercise the direct tea.Exec adapter independently
				x := &funcExec{run: func(*os.File, *os.File) error {
					if mode == "exec-left-stuck-reader" {
						go func() { defer close(childDone); _, _ = child.Read(make([]byte, 1)) }()
						<-child.entered
						keyread.HoldStuck(terminal.TTY, child.Close, childDone, keyread.ErrCannotCancel)
					}
					return nil
				}}
				first := func() tea.Msg {
					<-reader.entered
					return m.execWithInput(x, func(error) tea.Msg {
						callback.Add(1)
						return tea.QuitMsg{}
					})()
				}
				_, err := tea.NewProgram(execInitialModel{Model: pm, first: first}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = runWith(ctx, newFakeBackend(), Options{})
			var ue *client.UserError
			wantCode := "terminal_busy"
			if mode == "factory-fails" {
				wantCode = "terminal_failed"
			}
			if !errors.As(err, &ue) || ue.Code != wantCode || callback.Load() != 0 {
				t.Fatalf("runWith=%v (ue=%+v), callback=%d, want %s without ordinary callback", err, ue, callback.Load(), wantCode)
			}
			if mode == "factory-fails" && !errors.Is(err, factoryError) {
				t.Fatalf("resume lost factory cause: %v", err)
			}
			if mode != "factory-fails" && !errors.Is(err, keyread.ErrStuck) {
				t.Fatalf("resume lost stuck cause: %v", err)
			}
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("exec failure left terminal modes changed: %v", err)
			}
			terminal.WaitFor("\x1b[?1049l")
			reader.unblock()
			child.unblock()
			if mode == "exec-left-stuck-reader" {
				<-childDone
			}
			hosttest.WaitFor(t, 5*time.Second, "exec input cleanup", func() bool { return keyread.CheckStuck(terminal.TTY) == nil })
		})
	}
}

type execCancellableReader struct{ *demoHeldReader }

func (r *execCancellableReader) Cancel() bool {
	if r.cancels {
		r.unblock()
	}
	return r.demoHeldReader.Cancel()
}
