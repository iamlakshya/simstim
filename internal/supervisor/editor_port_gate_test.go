package supervisor

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These tests exercise a real private Unix grant and physical mux writer.
// They supply no native sandbox, original controller or editor proof.
type editorPortBlockedV1 struct {
	net.Conn
	armed    atomic.Bool
	entered  chan struct{}
	closed   chan struct{}
	writing  sync.Once
	closing  sync.Once
	returned atomic.Bool
}

func (c *editorPortBlockedV1) Write(data []byte) (int, error) {
	if !c.armed.Load() {
		return c.Conn.Write(data)
	}
	c.writing.Do(func() { close(c.entered) })
	<-c.closed
	c.returned.Store(true)
	return 0, net.ErrClosed
}
func (c *editorPortBlockedV1) Close() error {
	c.closing.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func editorPortGateFixtureV1(t *testing.T) (*Supervisor, *portForward, *link.Session, *editorPortBlockedV1) {
	t.Helper()
	testFolders(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	left, right := net.Pipe()
	writer := &editorPortBlockedV1{Conn: left, entered: make(chan struct{}), closed: make(chan struct{})}
	session, guest := link.New(writer, link.Supervisor, link.Options{}), link.New(right, link.Guest, link.Options{})
	t.Cleanup(func() { _ = session.Close(); _ = guest.Close() })
	// Complete the actual hello before the test stops a subsequent raw open.
	if _, err := session.Open(ctx, link.KindCtl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := guest.Accept(ctx); err != nil {
		t.Fatal(err)
	}
	policy := api.Policy{Order: []api.LayerRef{{Kind: api.LayerSession}}, Rules: []api.Rule{{Door: api.DoorPort, Layer: api.LayerSession, Target: "3000", Allow: true}}}
	owner := &Supervisor{id: "port2345", lifetime: ctx, link: session, m: &host.Manifest{Policy: &policy}, rec: record{phase: host.LifeRunning}}
	if err := owner.reconcilePorts(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { owner.closePortListeners(true) })
	owner.portMu.Lock()
	grant := owner.portListeners[3000]
	owner.portMu.Unlock()
	if grant == nil {
		t.Fatal("actual private grant was not issued")
	}
	return owner, grant, guest, writer
}

func TestPortRevocationInterruptsAdmittedRawWriteBeforeJoiningGate(t *testing.T) {
	owner, grant, _, writer := editorPortGateFixtureV1(t)
	writer.armed.Store(true)
	opened := make(chan error, 1)
	go func() {
		_, err := owner.link.OpenGuarded(grant.ctx, link.KindPort, proto.PortHeaderV1{Version: 1, Port: 3000}, func(ctx context.Context) (func(), error) { return owner.portGrantTurnV1(ctx, grant) })
		opened <- err
	}()
	<-writer.entered
	if grant.openMu.TryLock() {
		grant.openMu.Unlock()
		t.Fatal("actual physical open did not retain its original grant")
	}
	owner.mu.Lock()
	owner.m.Policy.Rules[0].Allow = false
	owner.mu.Unlock()
	revoked := make(chan error, 1)
	go func() { revoked <- owner.reconcilePorts() }()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("port revocation waited behind the later guest-link close")
	}
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Fatal("admitted revoked open did not report cancellation", err)
	}
	if !writer.returned.Load() {
		t.Fatal("grant was released before actual raw write joined")
	}
	if !grant.openMu.TryLock() {
		t.Fatal("revocation failed to join the original per-grant gate")
	}
	grant.openMu.Unlock()
}

func TestPortRevocationBeforeAdmissionKeepsOtherStreamsAndIDs(t *testing.T) {
	owner, grant, guest, _ := editorPortGateFixtureV1(t)
	grant.openMu.Lock()
	entered := make(chan struct{})
	opened := make(chan error, 1)
	go func() {
		_, err := owner.link.OpenGuarded(grant.ctx, link.KindPort, proto.PortHeaderV1{Version: 1, Port: 3000}, func(ctx context.Context) (func(), error) { close(entered); return owner.portGrantTurnV1(ctx, grant) })
		opened <- err
	}()
	<-entered
	owner.mu.Lock()
	owner.m.Policy.Rules[0].Allow = false
	owner.mu.Unlock()
	revoked := make(chan error, 1)
	go func() { revoked <- owner.reconcilePorts() }()
	select {
	case <-grant.ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revocation could not cancel a source waiting for its original gate")
	}
	grant.openMu.Unlock()
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if err := <-opened; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, end := context.WithTimeout(t.Context(), 3*time.Second)
	defer end()
	if _, err := owner.link.Open(ctx, link.KindExec, nil); err != nil {
		t.Fatal("unadmitted cancellation damaged another stream", err)
	}
	stream, err := guest.Accept(ctx)
	if err != nil || stream.Kind() != link.KindExec {
		t.Fatal("unadmitted cancellation consumed a contiguous wire ID", err)
	}
}
