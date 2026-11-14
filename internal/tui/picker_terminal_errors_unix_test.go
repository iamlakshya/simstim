//go:build darwin || linux

package tui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muesli/cancelreader"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/keyread"
)

type pickerFailureReader struct{ reads *atomic.Int32 }

func (r pickerFailureReader) Read([]byte) (int, error) { r.reads.Add(1); return 0, io.EOF }
func (pickerFailureReader) Cancel() bool               { return true }
func (pickerFailureReader) Close() error               { return nil }

// Return a non-UserError leaf reached through standard unwrap contracts.
// Requiring a nested leaf avoids accepting the wrapper's own message as proof
// that the underlying terminal error remains available to callers.
func pickerNestedCause(err error, nested bool) error {
	if err == nil {
		return nil
	}
	var multiple interface{ Unwrap() []error }
	if errors.As(err, &multiple) {
		for _, child := range multiple.Unwrap() {
			if cause := pickerNestedCause(child, true); cause != nil {
				return cause
			}
		}
		return nil
	}
	var single interface{ Unwrap() error }
	if errors.As(err, &single) {
		return pickerNestedCause(single.Unwrap(), true)
	}
	var user *client.UserError
	if nested && !errors.As(err, &user) {
		return err
	}
	return nil
}

func TestPickTerminalFailuresRemainActionableAndWrapped(t *testing.T) {
	for _, mode := range []string{"busy", "factory-error", "raw-error"} {
		t.Run(mode, func(t *testing.T) {
			terminal := newPickTerminal(t)
			in := terminal.TTY
			if mode == "raw-error" {
				var err error
				in, err = os.CreateTemp(t.TempDir(), "not-a-terminal")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = in.Close() })
			}
			oldOpen := keyread.Open
			var reads, opens, refreshes atomic.Int32
			failure := errors.New("picker reader factory failed")
			keyread.Open = func(io.Reader) (cancelreader.CancelReader, error) {
				opens.Add(1)
				if mode == "factory-error" {
					return nil, failure
				}
				return pickerFailureReader{&reads}, nil
			}
			var held chan struct{}
			if mode == "busy" {
				held = make(chan struct{})
				keyread.HoldStuck(in, func() error { return nil }, held, keyread.ErrCannotCancel)
			}
			t.Cleanup(func() {
				if held != nil {
					close(held)
				}
				_ = keyread.Release(in)
				keyread.Open = oldOpen
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := Pick(ctx, PickOptions{
				Title: "attach", In: in, Out: terminal.TTY, Rows: pickSample(),
				Refresh: func(context.Context) ([]PickRow, error) { refreshes.Add(1); return nil, nil },
			})
			want := "terminal_failed"
			if mode == "busy" {
				want = "terminal_busy"
			}
			var ue *client.UserError
			if !errors.As(err, &ue) || ue.Code != want || ue.Hint == "" || ue.Kind != client.KindFailed {
				t.Errorf("picker failure: err=%v ue=%+v, want %s with a Try hint", err, ue, want)
			}
			switch mode {
			case "busy":
				if !errors.Is(err, keyread.ErrStuck) || !errors.Is(err, keyread.ErrCannotCancel) || opens.Load() != 0 {
					t.Errorf("busy cause/factory lost: err=%v opens=%d", err, opens.Load())
				}
			case "factory-error":
				if !errors.Is(err, failure) {
					t.Errorf("factory cause is no longer wrapped: %v", err)
				}
			case "raw-error":
				cause := pickerNestedCause(err, false)
				if cause == nil || !strings.Contains(cause.Error(), "not a terminal") || !errors.Is(err, cause) {
					t.Errorf("raw-mode cause is no longer wrapped: err=%v cause=%v", err, cause)
				}
			}
			if reads.Load() != 0 || refreshes.Load() != 0 {
				t.Errorf("picker started work after terminal setup failed: reads=%d refreshes=%d", reads.Load(), refreshes.Load())
			}
		})
	}
}
