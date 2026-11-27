package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// startupProc closes its output before its exit status becomes available, as
// an SSH mux client does. The test controls the exit without a timing sleep.
type startupProc struct {
	in        startupInput
	out       io.Reader
	waiting   chan struct{}
	exit      chan struct{}
	killed    chan struct{}
	endOnce   sync.Once
	killOnce  sync.Once
	waitCalls atomic.Int32
	exitErr   error
	stderr    string
	afterKill <-chan struct{}
}

func newStartupProc(t *testing.T, writeErr error, exitErr error) *startupProc {
	t.Helper()
	p := &startupProc{in: startupInput{closed: make(chan struct{}), writeErr: writeErr},
		out: bytes.NewReader(nil), waiting: make(chan struct{}), exit: make(chan struct{}),
		killed: make(chan struct{}), exitErr: exitErr}
	t.Cleanup(p.end)
	return p
}

func (p *startupProc) Stdin() io.WriteCloser { return &p.in }
func (p *startupProc) Stdout() io.Reader     { return p.out }
func (p *startupProc) Stderr() string        { return p.stderr }
func (p *startupProc) Wait() error {
	if p.waitCalls.Add(1) == 1 {
		close(p.waiting)
	}
	select {
	case <-p.killed:
		if p.afterKill != nil {
			<-p.afterKill
		}
		return errors.New("transport killed")
	case <-p.exit:
		return p.exitErr
	}
}
func (p *startupProc) Kill() { p.killOnce.Do(func() { close(p.killed) }) }
func (p *startupProc) end()  { p.endOnce.Do(func() { close(p.exit) }) }

type startupInput struct {
	closed   chan struct{}
	once     sync.Once
	writeErr error
}

func (p *startupInput) Write(b []byte) (int, error) {
	if p.writeErr != nil {
		return 0, p.writeErr
	}
	return len(b), nil
}
func (p *startupInput) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

type startupTransport struct{ proc Proc }

func (t startupTransport) Start(context.Context, string) (Proc, error) { return t.proc, nil }
func (startupTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unexpected command")
}
func (startupTransport) ReachHint() string { return "Check the test transport." }

func TestDialKeepsDelayedTransportExit(t *testing.T) {
	longStartupGrace(t)
	for _, failure := range []struct {
		name  string
		write error
	}{{"output EOF", nil}, {"hello pipe closed", io.ErrClosedPipe}} {
		for _, verdict := range []struct {
			name   string
			exit   int
			stderr string
			code   string
		}{{"missing helper", 127, "the helper: not found", ""}, {"failed helper", 1, "helper startup failed", "helper_failed"},
			{"changed host key", 255, "", "host_key_changed"}} {
			t.Run(failure.name+"/"+verdict.name, func(t *testing.T) {
				sshDirs(t)
				p := newStartupProc(t, failure.write, exitErrCode(t, verdict.exit))
				p.stderr = verdict.stderr
				var process Proc = p
				var logFile *sshLogFile
				h := Host{Name: "box", SSH: "box.example"}
				if verdict.exit == 255 {
					pinFor(t, h.Name)
					p.exitErr = fmt.Errorf("%w: %w", ErrUnreachable, p.exitErr)
					dir := t.TempDir()
					logFile = &sshLogFile{dir: dir, path: filepath.Join(dir, "ssh.log")}
					process = &sshProc{Proc: p, logFile: logFile}
				}
				done := make(chan error, 1)
				go func() {
					_, err := dial(context.Background(), h, route{t: startupTransport{proc: process}})
					done <- err
				}()
				startupSignal(t, p.waiting, "the transport exit waiter")
				select {
				case <-p.killed:
					t.Fatal("the client killed the transport before collecting its exit")
				default:
				}
				select {
				case <-p.in.closed:
				default:
					t.Fatal("the client waited without closing stdin")
				}
				if logFile != nil {
					log := "Host key for " + pinAlias(h.Name) +
						" has changed and you have requested strict checking.\nHost key verification failed.\n"
					if err := os.WriteFile(logFile.path, []byte(log), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				p.end()
				err := startupError(t, done)
				if verdict.code == "" {
					if !errors.Is(err, ErrNotInstalled) {
						t.Fatalf("startup = %v, want missing helper", err)
					}
				} else {
					var ue *UserError
					if !errors.As(err, &ue) || ue.Code != verdict.code || ue.Hint == "" {
						t.Fatalf("startup = %#v, want %s with a hint", err, verdict.code)
					}
					if verdict.exit == 255 && ue.Kind != KindRefused {
						t.Fatalf("changed host key kind = %v, want refusal", ue.Kind)
					}
				}
				if got := p.waitCalls.Load(); got != 1 {
					t.Fatalf("Wait called %d times, want one", got)
				}
			})
		}
	}
}

func TestDialBoundsGraceForAStalledTransport(t *testing.T) {
	p := newStartupProc(t, nil, nil)
	done := make(chan error, 1)
	go func() {
		_, err := dial(context.Background(), Host{Name: "box"}, route{t: startupTransport{proc: p}})
		done <- err
	}()
	startupSignal(t, p.waiting, "the transport exit waiter")
	startupSignal(t, p.killed, "the bounded fallback kill")
	if err := startupError(t, done); err == nil {
		t.Fatal("a stalled transport succeeded")
	}
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

func TestDialCancellationInterruptsStartupGrace(t *testing.T) {
	longStartupGrace(t)
	p := newStartupProc(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: p}}); done <- err }()
	startupSignal(t, p.waiting, "the transport exit waiter")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup = %v, want cancellation", err)
	}
	select {
	case <-p.killed:
	default:
		t.Fatal("cancellation did not kill the transport")
	}
}

func TestDialCancellationAfterStartupGrace(t *testing.T) {
	p := newStartupProc(t, nil, nil)
	release := make(chan struct{})
	p.afterKill = release
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: p}}); done <- err }()
	startupSignal(t, p.killed, "the bounded fallback kill")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup = %v, want cancellation while exit cleanup waits", err)
	}
}

func TestDialRejectsAProtocolFrameWithoutGrace(t *testing.T) {
	longStartupGrace(t)
	p := newStartupProc(t, nil, nil)
	var frame bytes.Buffer
	if err := proto.NewWriter(&frame).Write(proto.TPing, nil); err != nil {
		t.Fatal(err)
	}
	p.out = &frame
	done := make(chan error, 1)
	go func() {
		_, err := dial(context.Background(), Host{Name: "box"}, route{t: startupTransport{proc: p}})
		done <- err
	}()
	err := startupError(t, done)
	if err == nil {
		t.Fatal("a ping was accepted as a hello")
	}
	select {
	case <-p.killed:
	default:
		t.Fatal("a bad protocol frame was not killed immediately")
	}
	startupSignal(t, p.waiting, "reaping the rejected transport")
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

func TestDialCancellationBeforeHelloReapsTransport(t *testing.T) {
	longStartupGrace(t)
	p := newStartupProc(t, nil, nil)
	reading := make(chan struct{})
	p.out = &startupBlockedOutput{reading: reading, stopped: p.killed}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { _, err := dial(ctx, Host{Name: "box"}, route{t: startupTransport{proc: p}}); done <- err }()
	startupSignal(t, reading, "the host hello reader")
	cancel()
	if err := startupError(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup = %v, want cancellation", err)
	}
	startupSignal(t, p.killed, "immediate transport kill")
	startupSignal(t, p.waiting, "reaping the cancelled transport")
	if got := p.waitCalls.Load(); got != 1 {
		t.Fatalf("Wait called %d times, want one", got)
	}
}

type startupBlockedOutput struct {
	reading chan struct{}
	stopped <-chan struct{}
}

func (r *startupBlockedOutput) Read([]byte) (int, error) {
	close(r.reading)
	<-r.stopped
	return 0, io.EOF
}

func TestDialRejectsAnInvalidFrameWithoutGrace(t *testing.T) {
	longStartupGrace(t)
	for _, frame := range []struct {
		name string
		data []byte
	}{{"empty frame", []byte{0, 0, 0, 0}}, {"partial header", []byte{1, 0}}} {
		t.Run(frame.name, func(t *testing.T) {
			p := newStartupProc(t, nil, nil)
			p.out = bytes.NewReader(frame.data)
			done := make(chan error, 1)
			go func() {
				_, err := dial(context.Background(), Host{Name: "box"}, route{t: startupTransport{proc: p}})
				done <- err
			}()
			if err := startupError(t, done); err == nil {
				t.Fatal("an invalid frame was accepted")
			}
			select {
			case <-p.killed:
			default:
				t.Fatal("an invalid frame was not killed immediately")
			}
		})
	}
}

func longStartupGrace(t *testing.T) {
	t.Helper()
	old := startupExitWait
	startupExitWait = time.Minute
	t.Cleanup(func() { startupExitWait = old })
}

func startupSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for " + what)
	}
}

func startupError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("the failed startup did not return")
		return nil
	}
}
