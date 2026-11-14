package proto

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
	"time"
)

type countFrameWriter struct {
	adjust int
	zero   bool
	err    error
	calls  int
	frame  []byte
}

func (w *countFrameWriter) Write(p []byte) (int, error) {
	w.calls++
	w.frame = append([]byte(nil), p...)
	if w.zero {
		return 0, w.err
	}
	return len(p) + w.adjust, w.err
}

func TestEveryWriterEntryRejectsIncompleteOrFailedFrameWrites(t *testing.T) {
	failed := errors.New("controlled frame write failure")
	for name, write := range map[string]func(*Writer) error{
		"Write":     func(w *Writer) error { return w.Write(TRequest, []byte("request")) },
		"WriteCtx":  func(w *Writer) error { return w.WriteCtx(context.Background(), TRequest, []byte("request")) },
		"WriteJSON": func(w *Writer) error { return w.WriteJSON(TRequest, map[string]string{"action": "request"}) },
		"WriteJSONCtx": func(w *Writer) error {
			return w.WriteJSONCtx(context.Background(), TRequest, map[string]string{"action": "request"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			for _, outcome := range []struct {
				name   string
				adjust int
				zero   bool
				err    error
			}{
				{"short nil error", -1, false, nil},
				{"zero nil error", 0, true, nil},
				{"invalid oversized count", 1, false, nil},
				{"short with error", -1, false, failed},
				{"full with error", 0, false, failed},
			} {
				t.Run(outcome.name, func(t *testing.T) {
					sink := &countFrameWriter{adjust: outcome.adjust, zero: outcome.zero, err: outcome.err}
					w := NewWriter(sink)
					err := write(w)
					want := failed
					if outcome.err == nil {
						want = io.ErrShortWrite
					}
					if !errors.Is(err, want) || sink.calls != 1 {
						t.Fatalf("frame result = %v, calls=%d, want %v without retry", err, sink.calls, want)
					}
					if len(sink.frame) < 5 || binary.LittleEndian.Uint32(sink.frame[:4]) != uint32(len(sink.frame)-4) || Type(sink.frame[4]) != TRequest {
						t.Fatalf("the sink did not receive exactly one complete frame: %x", sink.frame)
					}
					// An error releases the writer's turn for the next caller.
					sink.adjust, sink.err = 0, nil
					sink.zero = false
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					if err := w.WriteCtx(ctx, TRequest, []byte("retry")); err != nil || sink.calls != 2 {
						t.Fatalf("the failed frame retained its turn: %v, calls=%d", err, sink.calls)
					}
				})
			}
		})
	}
}

func TestCancelledContextWriterNeverCallsItsSink(t *testing.T) {
	sink := &countFrameWriter{}
	w := NewWriter(sink)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, write := range map[string]func() error{
		"WriteCtx":     func() error { return w.WriteCtx(ctx, TRequest, []byte("request")) },
		"WriteJSONCtx": func() error { return w.WriteJSONCtx(ctx, TRequest, map[string]string{"action": "request"}) },
	} {
		if err := write(); !errors.Is(err, context.Canceled) || sink.calls != 0 {
			t.Fatalf("%s = %v, sink calls=%d", name, err, sink.calls)
		}
	}
}
