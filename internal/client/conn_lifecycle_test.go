package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type lifecycleTransport struct {
	start func(context.Context) (Proc, error)
}

func (t lifecycleTransport) Start(ctx context.Context, _ string) (Proc, error) { return t.start(ctx) }
func (lifecycleTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unexpected command")
}
func (lifecycleTransport) ReachHint() string { return "Check the test transport." }

func TestDialCancelsABlockedTransportStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	entered := make(chan struct{})
	stopped := make(chan struct{})
	transport := lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}}
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, Host{Name: "box"}, route{t: transport}); done <- err }()
	startupSignal(t, entered, "the blocked transport start")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("dial = %v, want cancellation", err)
	}
	startupSignal(t, stopped, "the transport's startup cancellation")
}

func TestDialPreservesTheStartupDeadlineError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	t.Cleanup(cancel)
	transport := lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, err := dial(ctx, Host{Name: "box"}, route{t: transport})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial = %v, want the caller's deadline error", err)
	}
}

func TestDialKillsAndReapsALateTransportStart(t *testing.T) {
	p := newStartupProc(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStart := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseStart)
	transport := lifecycleTransport{start: func(context.Context) (Proc, error) {
		close(entered)
		<-release
		return p, nil
	}}
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, Host{Name: "box"}, route{t: transport}); done <- err }()
	startupSignal(t, entered, "the delayed transport start")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("dial = %v, want cancellation before Start returns", err)
	}
	releaseStart()
	startupSignal(t, p.killed, "killing the late process")
	startupSignal(t, p.waiting, "reaping the late process")
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

func TestDialReleasesTheContextAfterStartFailure(t *testing.T) {
	failure := errors.New("cannot start the transport")
	var observed context.Context
	transport := lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		observed = ctx
		return nil, failure
	}}
	_, err := dial(context.Background(), Host{Name: "box"}, route{t: transport})
	if !errors.Is(err, failure) {
		t.Fatalf("dial = %v, want the startup error", err)
	}
	startupSignal(t, observed.Done(), "releasing the failed startup context")
}

func TestDialKeepsAConnectionAfterItsCallerCancels(t *testing.T) {
	type valueKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), valueKey{}, "startup value"))
	t.Cleanup(cancel)
	p, output := helloStartupProc(t)
	var observed context.Context
	transport := lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		observed = ctx
		return p, nil
	}}
	c, err := dial(ctx, Host{Name: "box"}, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Value(valueKey{}) != "startup value" {
		t.Fatal("transport startup lost the caller's context values")
	}
	cancel()
	select {
	case <-observed.Done():
		t.Fatalf("an established connection retained its caller's cancellation: %v", observed.Err())
	case <-time.After(20 * time.Millisecond):
	}
	if err := c.Ping(); err != nil {
		t.Fatalf("ping after dial context cancellation: %v", err)
	}
	// End output before Close, so its already closed Done channel cannot
	// hide a missing release of the connection's own process context.
	_ = output.Close()
	startupSignal(t, c.Done(), "the closed bridge output")
	c.Close()
	startupSignal(t, observed.Done(), "releasing the closed connection's context")
	p.Kill()
	startupSignal(t, p.waiting, "the connection's single process waiter")
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

func TestDialReleasesTheContextWhenTheProcessEnds(t *testing.T) {
	p, _ := helloStartupProc(t)
	var observed context.Context
	transport := lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		observed = ctx
		return p, nil
	}}
	c, err := dial(context.Background(), Host{Name: "box"}, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	p.end()
	startupSignal(t, observed.Done(), "releasing the exited connection's context")
	startupSignal(t, c.Done(), "the exited connection")
}

func TestDialCancellationWinsOverAQueuedHello(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := newStartupProc(t, nil, nil)
	var hello bytes.Buffer
	writeStartupHello(t, &hello)
	p.out = &cancelHelloOutput{Reader: &hello, cancel: cancel}
	transport := lifecycleTransport{start: func(context.Context) (Proc, error) { return p, nil }}
	c, err := dial(ctx, Host{Name: "box"}, route{t: transport})
	if c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("dial = (%v, %v), want cancellation", c, err)
	}
	startupSignal(t, p.killed, "killing the cancelled hello process")
	startupSignal(t, p.waiting, "reaping the cancelled hello process")
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

type cancelHelloOutput struct {
	io.Reader
	cancel context.CancelFunc
}

func (r *cancelHelloOutput) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.cancel()
	return n, err
}

func helloStartupProc(t *testing.T) (*startupProc, *io.PipeWriter) {
	t.Helper()
	p := newStartupProc(t, nil, nil)
	var hello bytes.Buffer
	writeStartupHello(t, &hello)
	r, w := io.Pipe()
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	p.out = io.MultiReader(&hello, r)
	return p, w
}

func writeStartupHello(t *testing.T, w io.Writer) {
	t.Helper()
	if err := proto.NewWriter(w).WriteJSON(proto.THello,
		proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Role: "holder"}); err != nil {
		t.Fatal(err)
	}
}

func TestDialCancelsSSHStartupWhileThePinLockIsHeld(t *testing.T) {
	sshDirs(t)
	fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	pinFor(t, h.Name)
	unlock, err := lockPinWait(h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, h, route{t: &SSH{Host: h}}); done <- err }()
	if err := startupError(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dial waiting for the pin lock = %v, want the caller's deadline", err)
	}
}
