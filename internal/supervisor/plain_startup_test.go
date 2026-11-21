package supervisor

import (
	"bytes"
	"context"
	"errors"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// gated is a sandbox whose guest's words stop reaching the supervisor
// once the supervisor has sent start: the start's answer never comes. With
// stall, the guest's credit stops too, so the start's write waits.
type gated struct {
	sandbox
	sent  chan struct{}
	stall bool
}

func (g *gated) Start(ctx context.Context) (net.Conn, error) {
	c, err := g.sandbox.Start(ctx)
	if err != nil {
		return nil, err
	}
	return &gateConn{Conn: c, sent: g.sent, stall: g.stall, closed: make(chan struct{})}, nil
}

// gateConn drops what it reads after it wrote start, or with stall reads
// nothing more until it closes.
type gateConn struct {
	net.Conn
	sent   chan struct{}
	stall  bool
	once   sync.Once
	closed chan struct{}
	shut   sync.Once
}

func (c *gateConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"op":"start"`)) {
		c.once.Do(func() { close(c.sent) })
	}
	return c.Conn.Write(p)
}

func (c *gateConn) Read(p []byte) (int, error) {
	for {
		select {
		case <-c.sent:
			if c.stall {
				<-c.closed
				return 0, net.ErrClosed
			}
		default:
		}
		n, err := c.Conn.Read(p)
		select {
		case <-c.sent:
			if err != nil {
				return 0, err
			}
		default:
			return n, err
		}
	}
}

func (c *gateConn) Close() error {
	c.shut.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// gate makes each sandbox of the driver a gated one, and returns the
// channel that closes when the supervisor sends start, and the driver.
func gate(t *testing.T, stall bool) (sent chan struct{}, drivers <-chan *plain) {
	t.Helper()
	sent = make(chan struct{})
	got := make(chan *plain, 4)
	old := sandboxFor
	sandboxFor = func(m *host.Manifest) (sandbox, error) {
		sb, err := newSandbox(m)
		if p, ok := sb.(*plain); ok {
			got <- p
		}
		return &gated{sandbox: sb, sent: sent, stall: stall}, err
	}
	t.Cleanup(func() { sandboxFor = old })
	return sent, got
}

// A boot that ends before the start's answer, by a stop with the host or
// by its deadline, leaves no process of the sandbox: not the agent, nor a
// child of it, though the guest started them and they ignore SIGHUP and
// SIGTERM. The guest init is reaped, and the record says how the boot
// ended.
func TestAnInterruptedBootLeavesNoProcess(t *testing.T) {
	for _, c := range []struct {
		name string
		stop bool
		kind string
	}{
		{"a stop with the host", true, host.EndSystem},
		{"the boot's deadline", false, host.EndBoot},
	} {
		t.Run(c.name, func(t *testing.T) {
			signals := fakeSignals(t)
			old := bootTimeout
			bootTimeout = 4 * time.Second
			t.Cleanup(func() { bootTimeout = old })
			id, pids := plainSession(t)
			sent, drivers := gate(t, false)
			ended := make(chan error, 1)
			go func() { ended <- Run(id) }()
			sig := signals()
			select {
			case <-sent:
			case <-time.After(10 * time.Second):
				t.Fatal("no start")
			}
			agent, child := pids()
			if c.stop {
				sig <- syscall.SIGTERM
			}
			var err error
			select {
			case err = <-ended:
			case <-time.After(20 * time.Second):
				t.Fatal("the boot did not end")
			}
			goneWithin(t, 10*time.Second, agent, child)
			reaped(t, (<-drivers).cmd.Process.Pid)
			m, _ := host.LoadManifest(id)
			if err == nil || m.Life == nil || m.Life.End == nil || m.Life.End.Kind != c.kind || !m.Life.Started.IsZero() {
				t.Errorf("after the boot: %v, %+v", err, m.Life)
			}
		})
	}
}

// A stop with the host ends a start's write that waits for the guest's
// credit at once: the boot ends long before its deadline, and the guest
// init is reaped.
func TestAStopEndsAStartWriteThatWaits(t *testing.T) {
	signals := fakeSignals(t)
	id, _ := plainSession(t)
	m, _ := host.LoadManifest(id)
	m.Env = append(m.Env, "BIG="+strings.Repeat("x", 3*link.DefaultWindow))
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	sent, drivers := gate(t, true)
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	sig := signals()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("no start")
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	sig <- syscall.SIGTERM
	select {
	case err := <-ended:
		if !errors.Is(err, errStopped) || time.Since(start) > 15*time.Second {
			t.Errorf("a stop during the start's write = %v, after %s", err, time.Since(start))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stop did not end the start's write")
	}
	reaped(t, (<-drivers).cmd.Process.Pid)
}

// A stop with the host ends the wait for the session's secrets at once,
// and no sandbox starts after it.
func TestAStopEndsTheWaitForSecrets(t *testing.T) {
	signals := fakeSignals(t)
	id, _ := plainSession(t)
	m, _ := host.LoadManifest(id)
	m.Secrets = []string{"TEST_TOKEN"}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	var started atomic.Bool
	old := sandboxFor
	sandboxFor = func(*host.Manifest) (sandbox, error) {
		return &startSpy{started: &started}, nil
	}
	t.Cleanup(func() { sandboxFor = old })
	ended := make(chan error, 1)
	go func() { ended <- Run(id) }()
	sig := signals()
	waitFor(t, "the session's socket", func() bool {
		_, err := os.Stat(paths.Socket(id))
		return err == nil
	})
	start := time.Now()
	sig <- syscall.SIGTERM
	select {
	case err := <-ended:
		if !errors.Is(err, errStopped) || time.Since(start) > 5*time.Second {
			t.Errorf("a stop during the wait for secrets = %v, after %s", err, time.Since(start))
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stop did not end the wait for secrets")
	}
	if started.Load() {
		t.Error("a sandbox started after the stop")
	}
}

// startSpy is a sandbox that says whether it started.
type startSpy struct{ started *atomic.Bool }

func (s *startSpy) Start(context.Context) (net.Conn, error) {
	s.started.Store(true)
	return nil, errors.New("a spy starts nothing")
}
func (s *startSpy) Kill() error { return nil }
func (s *startSpy) Wait() error { return nil }
