//go:build !windows

package tui

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type demoPreparedConsoleReader struct {
	io.Reader
	started func()
	cancel  func() bool
	close   func() error
}

func (r demoPreparedConsoleReader) Read(p []byte) (int, error) {
	if r.started != nil {
		r.started()
	}
	return r.Reader.Read(p)
}

func (r demoPreparedConsoleReader) Cancel() bool { return r.cancel() }
func (r demoPreparedConsoleReader) Close() error { return r.close() }

func TestDemoRestoresConsoleModeBeforeAndAfterDeferredClose(t *testing.T) {
	for _, mode := range []string{"clean", "stuck", "factory-fails"} {
		t.Run(mode, func(t *testing.T) {
			terminal := hosttest.NewTerminal(t, 90, 30)
			fd := int(terminal.TTY.Fd())
			original, err := term.GetState(fd)
			if err != nil {
				t.Fatal(err)
			}
			held := newDemoHeldReader(false)
			readStarted := make(chan struct{})
			markReadStarted := sync.OnceFunc(func() { close(readStarted) })
			var closes atomic.Int32
			var captured *term.State
			factoryError := errors.New("demo console reader failed after preparation")
			oldFactory := openDemoAttachReader
			openDemoAttachReader = func(io.Reader) (cancelreader.CancelReader, error) {
				captured, err = term.GetState(fd)
				if err != nil {
					return nil, err
				}
				if _, err := term.MakeRaw(fd); err != nil {
					return nil, err
				}
				if mode == "factory-fails" {
					return nil, factoryError
				}
				var source io.Reader = strings.NewReader("\x1c")
				cancel := func() bool { return true }
				if mode == "stuck" {
					source = held
					cancel = held.Cancel
				}
				return demoPreparedConsoleReader{Reader: source, started: markReadStarted, cancel: cancel, close: func() error {
					closes.Add(1)
					if mode == "stuck" {
						_ = held.Close()
					}
					return term.Restore(fd, captured)
				}}, nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var done <-chan struct {
				res client.AttachResult
				err error
			}
			t.Cleanup(func() {
				defer func() { openDemoAttachReader = oldFactory }()
				cancel()
				held.unblock()
				if done != nil {
					for range done {
					}
				}
				hosttest.WaitFor(t, 5*time.Second, "the demo console reader to leave", func() bool {
					return keyread.CheckStuck(terminal.TTY) == nil
				})
				_ = keyread.Release(terminal.TTY)
			})
			done = startDemoAttach(t, ctx, terminal.TTY)
			if mode != "factory-fails" {
				select {
				case <-readStarted:
				case <-ctx.Done():
					select {
					case <-readStarted:
					default:
						t.Fatal("demo did not request its initial input read")
					}
				}
			}
			if mode == "stuck" {
				select {
				case <-held.entered:
				case <-ctx.Done():
					select {
					case <-held.entered:
					default:
						t.Fatal("demo initial input read did not reach the held reader")
					}
				}
				cancel()
			}
			result, err := demoAttachResult(t, done)
			if mode == "factory-fails" {
				if !errors.Is(err, factoryError) {
					t.Fatalf("factory error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(original, captured) {
				t.Error("reader factory captured an already changed mode")
			}
			if mode == "clean" && ctx.Err() != nil {
				t.Fatalf("clean detach needed context cancellation: %v", ctx.Err())
			}
			if mode == "clean" && !result.Detached {
				t.Fatalf("clean result=%+v", result)
			}
			if mode == "stuck" && (!result.ReaderStuck || closes.Load() != 0) {
				t.Fatalf("stuck result=%+v closes=%d", result, closes.Load())
			}
			assertMode := func() {
				t.Helper()
				after, err := term.GetState(fd)
				if err != nil || !reflect.DeepEqual(original, after) {
					t.Fatalf("demo console mode was not restored: %v", err)
				}
			}
			assertMode()
			if mode == "stuck" {
				held.unblock()
				hosttest.WaitFor(t, 5*time.Second, "deferred demo console close", func() bool { return keyread.CheckStuck(terminal.TTY) == nil })
				held.mu.Lock()
				under := held.closedDuringRead
				held.mu.Unlock()
				if under {
					t.Fatal("deferred Close ran under an active Read")
				}
				assertMode()
			}
			if mode != "factory-fails" && closes.Load() != 1 {
				t.Fatalf("closes=%d", closes.Load())
			}
		})
	}
}
