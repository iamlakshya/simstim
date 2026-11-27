package egress

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

type boundDeadlineWriter struct {
	conn net.Conn
}

func (*boundDeadlineWriter) Header() http.Header { return make(http.Header) }
func (*boundDeadlineWriter) WriteHeader(int)     {}
func (w *boundDeadlineWriter) Write(p []byte) (int, error) {
	if w.conn == nil {
		return len(p), nil
	}
	return w.conn.Write(p)
}
func (w *boundDeadlineWriter) SetWriteDeadline(at time.Time) error {
	if w.conn != nil {
		return w.conn.SetWriteDeadline(at)
	}
	return nil
}

func boundPrimaryUse(server *Server, names ...string) secretRequestUse {
	use := secretRequestUse{Names: names}
	for _, name := range names {
		use.Identities = append(use.Identities, server.currentMembers().secrets[name].identity)
	}
	return use
}

func TestBoundRequestAbortInterruptsBlockedGuestWriteBeforePhysicalJoin(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	guest, peer := net.Pipe()
	t.Cleanup(func() { _ = guest.Close(); _ = peer.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	request := &originRequest{s: server, request: Request{Host: "example.com", Port: 443}, permit: server.currentEngine().NewPermit(), ctx: ctx, cancel: cancel, http2: true}
	writer := &boundDeadlineWriter{conn: guest}
	if err := request.bindResponseWriter(writer); err != nil || !request.track() {
		t.Fatal("response writer ownership was not admitted")
	}
	server.policyMu.Lock()
	err := request.tagSecretNamesLocked([]string{"KEY"})
	server.policyMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	entered, finished := make(chan struct{}), make(chan struct{})
	go func() {
		close(entered)
		_, _ = writer.Write([]byte("guest flow control stalled"))
		request.close()
		close(finished)
	}()
	<-entered
	done := make(chan error, 1)
	go func() { done <- server.closeSecretResources(t.Context(), []string{"KEY"}, nil, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		_ = guest.Close()
		t.Fatal("closing the origin did not interrupt the blocked guest response write")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("join completed before the actual response writer owner finished")
	}
}

type boundOrderConn struct {
	net.Conn
	mu     sync.Mutex
	closed bool
}

func (c *boundOrderConn) Close() error {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	return c.Conn.Close()
}

func TestBoundRequestClosesOriginBeforeDecoderCancellation(t *testing.T) {
	origin, peer := net.Pipe()
	defer peer.Close()
	owned := &boundOrderConn{Conn: origin}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelSawClosed := false
	request := &originRequest{ctx: ctx, conn: owned, cancel: func() {
		owned.mu.Lock()
		cancelSawClosed = owned.closed
		owned.mu.Unlock()
		cancel()
	}}
	request.stopIO()
	if !cancelSawClosed {
		t.Fatal("decoder cancellation could precede closing its owned origin socket")
	}
}

func TestBoundH2OwnershipRefusesMissingResponseWriterDeadlineHook(t *testing.T) {
	server, _ := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := &originRequest{s: server, request: Request{Host: "example.com", Port: 443}, permit: server.currentEngine().NewPermit(), ctx: ctx, cancel: cancel, http2: true}
	if request.track() {
		t.Fatal("H2 request gained a joinable owner without a write interruption hook")
	}
}
