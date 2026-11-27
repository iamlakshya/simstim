package proto

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type guardEncodeSignal struct {
	encoded chan struct{}
	calls   *atomic.Int32
}

func (v guardEncodeSignal) MarshalJSON() ([]byte, error) {
	v.calls.Add(1)
	close(v.encoded)
	return []byte(`{"request":"original"}`), nil
}

func TestGuardedJSONChecksChangedSourceOnlyAfterActualWriterTurn(t *testing.T) {
	var sink bytes.Buffer
	writer := NewWriter(&sink)
	writer.turn <- struct{}{}
	encoded := make(chan struct{})
	called := make(chan struct{})
	done := make(chan error, 1)
	var epoch, encodes atomic.Int32
	epoch.Store(1)
	refused := errors.New("original source changed while queued")
	go func() {
		done <- writer.WriteJSONGuardedCtx(t.Context(), TRequest, guardEncodeSignal{encoded, &encodes}, func() error {
			close(called)
			if epoch.Load() != 1 {
				return refused
			}
			return nil
		})
	}()
	<-encoded
	select {
	case <-called:
		t.Fatal("source guard ran before writer ownership")
	default:
	}
	epoch.Store(2)
	<-writer.turn
	if err := <-done; !errors.Is(err, refused) {
		t.Fatal("changed queued source did not preserve refusal", err)
	}
	if sink.Len() != 0 || encodes.Load() != 1 {
		t.Fatal("refused frame wrote bytes or encoded twice", sink.Len(), encodes.Load())
	}
	if err := writer.WriteCtx(t.Context(), TPing, nil); err != nil {
		t.Fatal("refusal retained writer turn", err)
	}
}

func TestGuardedJSONCancellationAfterGuardWritesNothing(t *testing.T) {
	var sink bytes.Buffer
	writer := NewWriter(&sink)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	err := writer.WriteJSONGuardedCtx(ctx, TRequest, struct{}{}, func() error { calls++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || calls != 1 || sink.Len() != 0 {
		t.Fatal("cancelled guard wrote a frame", err, calls, sink.Len())
	}
	if err := writer.WriteJSONCtx(t.Context(), TPing, struct{}{}); err != nil {
		t.Fatal("cancelled guard retained writer turn", err)
	}
}

func TestGuardedJSONPreservesExactFramingAndSizeBeforeGuard(t *testing.T) {
	var original, guarded bytes.Buffer
	value := map[string]string{"x": "original"}
	if err := NewWriter(&original).WriteJSONCtx(t.Context(), TRequest, value); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := NewWriter(&guarded).WriteJSONGuardedCtx(t.Context(), TRequest, value, func() error { calls++; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Equal(original.Bytes(), guarded.Bytes()) {
		t.Fatal("guard changed framing or ran more than once")
	}
	before := guarded.Len()
	if err := NewWriter(&guarded).WriteJSONGuardedCtx(t.Context(), TRequest, strings.Repeat("x", MaxFrame), func() error { calls++; return nil }); !errors.Is(err, ErrFrameTooBig) {
		t.Fatal("oversized guarded frame changed its bound", err)
	}
	if calls != 1 || guarded.Len() != before {
		t.Fatal("oversized frame invoked guard or wrote bytes")
	}
}
