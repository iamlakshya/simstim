//go:build !windows

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type preparedConsoleReader struct {
	io.Reader
	cancel func() bool
	close  func() error
}

func (r preparedConsoleReader) Cancel() bool { return r.cancel() }
func (r preparedConsoleReader) Close() error { return r.close() }

// Windows readers prepare the console in their factory and reset it when
// closed. Model both operations on a PTY so the saved-state order is tested
// on Unix too, including a reader which must remain open after the command.
func TestKeyTesterRestoresTheStateBeforeItsReaderFactory(t *testing.T) {
	for _, mode := range []string{"clean-exit", "stuck-read", "factory-error"} {
		t.Run(mode, func(t *testing.T) {
			hosttest.New(t)
			withSignal(t)
			terminal := hosttest.NewTerminal(t, 90, 30)
			fd := int(terminal.TTY.Fd())
			original, err := term.GetState(fd)
			if err != nil {
				t.Fatal(err)
			}
			oldIn, oldOut, oldFactory := os.Stdin, os.Stdout, newKeyReader
			os.Stdin, os.Stdout = terminal.TTY, terminal.TTY
			held := newHeldKeyReader(false)
			var closes atomic.Int32
			var closeSawRaw atomic.Bool
			newKeyReader = func(f *os.File) (cancelreader.CancelReader, error) {
				beforeFactory, err := term.MakeRaw(int(f.Fd()))
				if err != nil {
					return nil, fmt.Errorf("prepare the test console: %w", err)
				}
				prepared, err := term.GetState(int(f.Fd()))
				if err != nil || reflect.DeepEqual(beforeFactory, prepared) {
					return nil, errors.New("the test factory did not change console modes")
				}
				if mode == "factory-error" {
					return nil, errors.New("the prepared reader could not open")
				}
				var reader io.Reader = strings.NewReader("qq")
				cancel := func() bool { return true }
				if mode == "stuck-read" {
					reader, cancel = held, held.Cancel
				}
				return preparedConsoleReader{
					Reader: reader,
					cancel: cancel,
					close: func() error {
						closes.Add(1)
						atClose, _ := term.GetState(fd)
						closeSawRaw.Store(!reflect.DeepEqual(atClose, original))
						if mode == "stuck-read" {
							_ = held.Close()
						}
						return term.Restore(fd, beforeFactory)
					},
				}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			ended := make(chan struct{})
			go func() {
				defer close(ended)
				result <- keyTester(ctx)
			}()
			t.Cleanup(func() {
				cancel()
				held.letGo()
				<-ended
				hosttest.WaitFor(t, 5*time.Second, "the console reader to release", func() bool {
					return keyread.CheckStuck(terminal.TTY) == nil
				})
				_ = keyread.Release(terminal.TTY)
				_ = term.Restore(fd, original)
				os.Stdin, os.Stdout, newKeyReader = oldIn, oldOut, oldFactory
			})
			if mode == "stuck-read" {
				select {
				case <-held.started:
				case <-time.After(5 * time.Second):
					t.Fatal("the console read did not start")
				}
				cancel()
			}
			select {
			case err := <-result:
				switch mode {
				case "clean-exit":
					if err != nil || closes.Load() != 1 || !closeSawRaw.Load() {
						t.Errorf("clean exit: err=%v closes=%d close-before-original-restore=%v", err, closes.Load(), closeSawRaw.Load())
					}
				case "stuck-read":
					if !errors.Is(err, context.Canceled) || closes.Load() != 0 {
						t.Errorf("stuck exit: err=%v closes=%d", err, closes.Load())
					}
					if err := keyread.CheckStuck(terminal.TTY); !errors.Is(err, keyread.ErrStuck) {
						t.Errorf("the stuck reader was not kept: %v", err)
					}
				case "factory-error":
					var ue *client.UserError
					if !errors.As(err, &ue) || ue.Code != "terminal_failed" {
						t.Errorf("factory failure: err=%v ue=%+v", err, ue)
					}
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the key tester did not end")
			}
			<-ended
			after, err := term.GetState(fd)
			if err != nil || !reflect.DeepEqual(original, after) {
				t.Errorf("key tester restored the factory's state, not the original: before=%+v after=%+v err=%v", original, after, err)
			}
		})
	}
}

func TestKeyTesterRefusesAStuckReaderBeforePreparingTheConsole(t *testing.T) {
	hosttest.New(t)
	terminal := hosttest.NewTerminal(t, 90, 30)
	fd := int(terminal.TTY.Fd())
	original, err := term.GetState(fd)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	keyread.HoldStuck(terminal.TTY, func() error { return nil }, done, keyread.ErrCannotCancel)
	oldIn, oldOut, oldFactory := os.Stdin, os.Stdout, newKeyReader
	os.Stdin, os.Stdout = terminal.TTY, terminal.TTY
	var calls atomic.Int32
	newKeyReader = func(*os.File) (cancelreader.CancelReader, error) {
		calls.Add(1)
		return nil, errors.New("must not open another console reader")
	}
	t.Cleanup(func() {
		close(done)
		_ = keyread.Release(terminal.TTY)
		os.Stdin, os.Stdout, newKeyReader = oldIn, oldOut, oldFactory
	})
	err = keyTester(context.Background())
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "terminal_busy" || calls.Load() != 0 {
		t.Errorf("stuck reader: err=%v ue=%+v factory calls=%d", err, ue, calls.Load())
	}
	after, err := term.GetState(fd)
	if err != nil || !reflect.DeepEqual(original, after) {
		t.Errorf("the refused command changed console modes: %v", err)
	}
}
