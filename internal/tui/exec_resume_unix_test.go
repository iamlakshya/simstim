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

	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func TestExecResumesKeyboardAfterOrdinaryCommandResult(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "success"
		if failed {
			name = "command-error"
		}
		t.Run(name, func(t *testing.T) {
			terminal := hosttest.NewTerminal(t, 90, 30)
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			first, next := newDemoHeldReader(true), newDemoHeldReader(true)
			var opens atomic.Int32
			restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
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
			commandError := errors.New("command failed without breaking input")
			var wantError error
			if failed {
				wantError = commandError
			}
			results := make(chan error, 1)
			runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
				m := pm.(*framed).m.(*Model)
				m.handoff = false // exercise the direct tea.Exec adapter independently
				x := &funcExec{run: func(*os.File, *os.File) error { return wantError }}
				start := func() tea.Msg {
					<-first.entered
					return m.execWithInput(x, func(err error) tea.Msg { results <- err; return nil })()
				}
				_, err := tea.NewProgram(execInitialModel{Model: pm, first: start}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			typed := make(chan struct{})
			go func() {
				defer close(typed)
				select {
				case <-next.entered:
				case <-ctx.Done():
					return
				}
				select {
				case next.input <- []byte("q"):
				case <-ctx.Done():
				}
			}()
			if err := runWith(ctx, newFakeBackend(), Options{}); err != nil {
				t.Fatalf("ordinary command result ended the launcher: %v", err)
			}
			<-typed
			select {
			case got := <-results:
				if !errors.Is(got, wantError) {
					t.Fatalf("callback got %v, want %v", got, wantError)
				}
			default:
				t.Fatal("ordinary command callback did not run")
			}
			if ctx.Err() != nil || opens.Load() != 2 {
				t.Fatalf("launcher never resumed keyboard: ctx=%v, readers=%d", ctx.Err(), opens.Load())
			}
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("resumed launcher left terminal modes changed: %v", err)
			}
		})
	}
}
