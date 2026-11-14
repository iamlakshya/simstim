//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

// Windows CancelReader factories change input mode immediately. Model that
// side effect on a real Unix terminal, including a reader whose Close cannot
// safely run yet and a factory that fails after changing the mode.
func TestLauncherRestoresModeFromBeforeReaderFactory(t *testing.T) {
	for _, mode := range []string{"clean", "stuck", "factory-fails"} {
		t.Run(mode, func(t *testing.T) {
			terminal := hosttest.NewTerminal(t, 90, 30)
			before, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			reader := newDemoHeldReader(mode != "stuck")
			factoryError := errors.New("factory changed mode then failed")
			restoreReader := SetLauncherReader(func(io.Reader) (cancelreader.CancelReader, error) {
				if _, err := term.MakeRaw(int(terminal.TTY.Fd())); err != nil {
					return nil, err
				}
				if mode == "factory-fails" {
					return nil, factoryError
				}
				if mode == "stuck" {
					return reader, nil
				}
				return &execCancellableReader{reader}, nil
			})
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
			runProgram = func(ctx context.Context, pm tea.Model, in io.Reader, out io.Writer) error {
				_, err := tea.NewProgram(execInitialModel{Model: pm, first: func() tea.Msg { <-reader.entered; return tea.QuitMsg{} }}, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(out), tea.WithoutSignalHandler()).Run()
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = runWith(ctx, newFakeBackend(), Options{})
			if mode == "factory-fails" {
				if !errors.Is(err, factoryError) {
					t.Fatalf("factory error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := term.GetState(int(terminal.TTY.Fd()))
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("%s left factory mode installed: %v", mode, err)
			}
			reader.mu.Lock()
			closedEarly, closes := reader.closedDuringRead, reader.closes
			reader.mu.Unlock()
			if closedEarly || (mode == "stuck" && closes != 0) {
				t.Fatalf("reader closed while live: early=%v closes=%d", closedEarly, closes)
			}
			if mode == "stuck" {
				if !errors.Is(keyread.CheckStuck(terminal.TTY), keyread.ErrStuck) {
					t.Fatal("stuck reader untracked")
				}
				reader.unblock()
				<-reader.left
			}
		})
	}
}
