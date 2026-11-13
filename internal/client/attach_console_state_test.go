//go:build !windows

package client_test

import (
	"context"
	"errors"
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

type attachPreparedConsoleReader struct {
	io.Reader
	cancel func() bool
	close  func() error
}

func (r attachPreparedConsoleReader) Cancel() bool { return r.cancel() }
func (r attachPreparedConsoleReader) Close() error { return r.close() }

// A console reader captures and later restores its construction mode. Its
// delayed Close must preserve the original cooked mode after a stuck attach.
func TestAttachRestoresConsoleModeBeforeAndAfterDeferredClose(t *testing.T) {
	for _, mode := range []string{"clean", "stuck", "factory-fails"} {
		t.Run(mode, func(t *testing.T) {
			r := newAttachRun(t)
			original, err := term.GetState(int(r.term.TTY.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			held := &nonCancellingTTYReader{entered: make(chan struct{}, 2), release: make(chan struct{})}
			release := func() {
				select {
				case <-held.release:
				default:
					close(held.release)
				}
			}
			var closes atomic.Int32
			var captured *term.State
			factoryError := errors.New("console reader factory failed after preparation")
			restore := client.SetAttachReader(func(in io.Reader) (cancelreader.CancelReader, error) {
				fd := int(in.(*os.File).Fd())
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
				var source io.Reader = strings.NewReader(detach)
				cancel := func() bool { return true }
				if mode == "stuck" {
					source = held
					cancel = held.Cancel
				}
				return attachPreparedConsoleReader{Reader: source, cancel: cancel, close: func() error {
					closes.Add(1)
					if mode == "stuck" {
						_ = held.Close()
					}
					return term.Restore(fd, captured)
				}}, nil
			})
			t.Cleanup(func() { release(); _ = keyread.Release(r.term.TTY); restore() })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			done := r.attach(ctx, r.session("exec sleep 60"), client.DefaultSettings())
			if mode == "stuck" {
				<-held.entered
				cancel()
			}
			result := r.end(done)
			if mode == "factory-fails" {
				if !errors.Is(result.err, factoryError) {
					t.Fatalf("factory error=%v", result.err)
				}
			} else if result.err != nil {
				t.Fatal(result.err)
			}
			if !reflect.DeepEqual(original, captured) {
				t.Error("reader factory captured an already changed mode")
			}
			if mode == "clean" && ctx.Err() != nil {
				t.Fatalf("clean detach needed context cancellation: %v", ctx.Err())
			}
			if mode == "clean" && !result.res.Detached {
				t.Fatalf("clean result=%+v", result.res)
			}
			if mode == "stuck" && (!result.res.ReaderStuck || closes.Load() != 0) {
				t.Fatalf("stuck result=%+v closes=%d", result.res, closes.Load())
			}
			assertMode := func() {
				t.Helper()
				after, err := term.GetState(int(r.term.TTY.Fd()))
				if err != nil || !reflect.DeepEqual(original, after) {
					t.Fatalf("console mode was not restored: %v", err)
				}
			}
			assertMode()
			if mode == "stuck" {
				release()
				hosttest.WaitFor(t, 5*time.Second, "deferred attach close", func() bool { return keyread.CheckStuck(r.term.TTY) == nil })
				held.mu.Lock()
				under := held.under
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
