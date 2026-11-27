package egress

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type retiringDeadlineWriter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int64
}

func (*retiringDeadlineWriter) Header() http.Header         { return make(http.Header) }
func (*retiringDeadlineWriter) WriteHeader(int)             {}
func (*retiringDeadlineWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *retiringDeadlineWriter) SetWriteDeadline(at time.Time) error {
	if at.IsZero() {
		return nil
	}
	w.calls.Add(1)
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return nil
}

func retiringWriterScope(t *testing.T) (*originRequest, *retiringDeadlineWriter, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	writer := &retiringDeadlineWriter{entered: make(chan struct{}), release: make(chan struct{})}
	release := sync.OnceFunc(func() { close(writer.release) })
	t.Cleanup(release)
	scope := &originRequest{http2: true, ctx: ctx, cancel: cancel}
	if err := scope.bindResponseWriter(writer); err != nil {
		t.Fatal(err)
	}
	return scope, writer, release
}

func TestResponseWriterRetirementJoinsAnActiveDeadlineAndRefusesLaterUse(t *testing.T) {
	scope, writer, release := retiringWriterScope(t)
	aborted, retired := make(chan struct{}), make(chan struct{})
	retireStarted := false
	go func() { scope.abort(); close(aborted) }()
	t.Cleanup(func() {
		release()
		waitHTTPProgress(t, aborted, "the owned deadline abort")
		if retireStarted {
			waitHTTPProgress(t, retired, "the owned response writer retirement")
		}
	})
	waitHTTPProgress(t, writer.entered, "the actual blocked deadline operation")
	retireStarted = true
	go func() { scope.releaseResponseWriter(); close(retired) }()
	select {
	case <-retired:
		t.Fatal("writer retirement returned while its actual deadline operation still ran")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	waitHTTPProgress(t, retired, "the response writer retirement after deadline completion")
	waitHTTPProgress(t, aborted, "the completed abort")
	scope.abort()
	if writer.calls.Load() != 1 {
		t.Fatal("an abort touched a retired handler-owned writer")
	}
}

func TestClientCancellationStopJoinsItsStartedAbortBeforeHandlerReturn(t *testing.T) {
	scope, writer, release := retiringWriterScope(t)
	client, cancel := context.WithCancel(t.Context())
	defer cancel()
	join := scope.watchClientCancellation(client)
	joined := make(chan struct{})
	joinStarted := false
	t.Cleanup(func() {
		release()
		if joinStarted {
			waitHTTPProgress(t, joined, "the owned client cancellation join")
		} else {
			join()
		}
	})
	cancel()
	waitHTTPProgress(t, writer.entered, "the actual client cancellation deadline operation")
	joinStarted = true
	go func() { join(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("handler cleanup treated an already-started cancellation as joined")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	waitHTTPProgress(t, joined, "the exact client cancellation callback completion")
	if scope.ctx.Err() == nil {
		t.Fatal("the cancellation join returned before its scope abort completed")
	}
	scope.releaseResponseWriter()
	join() // cleanup is also safe after the watcher was already joined
	scope.abort()
	if writer.calls.Load() != 1 {
		t.Fatal("late client cleanup reused a completed handler's writer")
	}
}
