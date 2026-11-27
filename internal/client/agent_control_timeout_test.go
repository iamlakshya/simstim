package client

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type controlTimeoutConn struct {
	net.Conn
	blocked chan struct{}
	stop    chan struct{}
	start   sync.Once
	closed  sync.Once
}

func (c *controlTimeoutConn) Write(p []byte) (int, error) {
	if len(p) > 4 && proto.Type(p[4]) == proto.TStreamData {
		c.start.Do(func() { close(c.blocked) })
		<-c.stop
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

func (c *controlTimeoutConn) Close() error {
	c.closed.Do(func() { close(c.stop) })
	return c.Conn.Close()
}

func TestAgentControlBackgroundContextHasItsOwnPublicationTimeout(t *testing.T) {
	if agentbridge.HandshakeTimeout != 10*time.Second {
		t.Fatal("the production agent handshake bound changed")
	}
	client, server := net.Pipe()
	blocked := &controlTimeoutConn{Conn: client, blocked: make(chan struct{}), stop: make(chan struct{})}
	supervisor := link.New(blocked, link.Supervisor, agentbridge.Options())
	guest := link.New(server, link.Guest, agentbridge.Options())
	finished := make(chan struct{})
	started := false
	t.Cleanup(func() {
		// The caller still owns aborting the whole mux on Flush failure.
		_ = supervisor.Close()
		_ = guest.Close()
		if started {
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Error("control did not leave after its owned mux closed")
			}
		}
	})
	scope := agentbridge.Scope{Session: "abc23def", Lease: "0123456789abcdef0123456789abcdef"}
	setup, cancelSetup := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelSetup()
	control, err := supervisor.Open(setup, link.KindCtl, scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guest.Accept(setup); err != nil {
		t.Fatal(err)
	}
	// This peer deliberately sets no control deadline and sends no reply.
	// An honest host's own 10 s read timeout would mask the client regression.
	a := &attachedAgent{scope: scope, ctl: control, seq: 1, turn: make(chan struct{}, 1), done: make(chan struct{})}
	type outcome struct {
		err     error
		elapsed time.Duration
	}
	result := make(chan outcome, 1)
	start := time.Now()
	started = true
	go func() {
		defer close(finished)
		err := a.control(context.Background(), "activate", nil)
		result <- outcome{err, time.Since(start)}
	}()
	// A second control call waits for its turn without reaching a stream.
	// Both waits run together, so this keeps the real bound without doubling
	// the test duration or lowering the production timeout.
	queued := &attachedAgent{scope: scope, seq: 1, turn: make(chan struct{}, 1), done: make(chan struct{})}
	queued.turn <- struct{}{}
	queueResult, queueFinished := make(chan outcome, 1), make(chan struct{})
	t.Cleanup(func() {
		close(queued.done)
		select {
		case <-queueFinished:
		case <-time.After(5 * time.Second):
			t.Error("queued control did not leave after revocation")
		}
	})
	go func() {
		defer close(queueFinished)
		err := queued.control(context.Background(), "activate", nil)
		queueResult <- outcome{err, time.Since(start)}
	}()
	select {
	case <-blocked.blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("control did not reach the blocked activation writer")
	}
	deadline := time.NewTimer(time.Until(start.Add(13 * time.Second)))
	defer deadline.Stop()
	for name, result := range map[string]<-chan outcome{"writer": result, "turn": queueResult} {
		select {
		case got := <-result:
			if !errors.Is(got.err, context.DeadlineExceeded) {
				t.Fatalf("background %s wait did not report its own deadline: %v", name, got.err)
			}
			if got.elapsed < 10*time.Second {
				t.Fatalf("the production %s timeout was shortened in the proof: %v", name, got.elapsed)
			}
		case <-deadline.C:
			t.Fatalf("background %s wait exceeded its handshake bound", name)
		}
	}
}

func TestCancelledAgentControlDoesNotConsumeATurnOrReportSequenceState(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := &attachedAgent{seq: ^uint64(0), turn: make(chan struct{}, 1), done: make(chan struct{})}
	// A ready turn and cancelled context may both be selected. An exhausted
	// sequence makes the wrong branch observable without any stream or I/O.
	for range 256 {
		if err := a.control(ctx, "activate", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled control continued past its turn: %v", err)
		}
		if len(a.turn) != 0 {
			t.Fatal("cancelled control retained the next caller's turn")
		}
	}
}
