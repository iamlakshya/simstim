package supervisor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// delayedDeadlineConn keeps the test's clock stopped until both slow clients
// occupy their relay buffers. Once released, only an installed write deadline
// advances. A missing deadline remains blocked until test cleanup closes it.
type delayedDeadlineConn struct {
	net.Conn
	mu       sync.Mutex
	deadline time.Time
	writing  chan struct{}
	once     sync.Once
}

func (c *delayedDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	c.deadline = deadline
	c.mu.Unlock()
	if deadline.IsZero() {
		return c.Conn.SetWriteDeadline(deadline)
	}
	return nil
}

func (c *delayedDeadlineConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

func (c *delayedDeadlineConn) advance() {
	c.mu.Lock()
	set := !c.deadline.IsZero()
	c.mu.Unlock()
	if set {
		_ = c.Conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	}
}

// Two clients that stop reading cannot keep the shared relay budget forever.
// The third client gets its complete snapshot after the slow writes expire.
func TestStalledClientWritesReleaseTheRelayBudget(t *testing.T) {
	a, b := net.Pipe()
	sup := link.New(a, link.Supervisor, link.Options{})
	gst := link.New(b, link.Guest, link.Options{})
	t.Cleanup(func() { _ = sup.Close(); _ = gst.Close() })
	s := &Supervisor{link: sup}
	var relays sync.WaitGroup
	var clients []*client
	t.Cleanup(func() {
		for _, c := range clients {
			c.close()
		}
		_ = sup.Close()
		_ = gst.Close()
		relays.Wait()
	})
	open := func(conn net.Conn) *link.Stream {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		accepted := make(chan *link.Stream, 1)
		go func() { st, _ := gst.Accept(ctx); accepted <- st }()
		st, err := sup.Open(ctx, link.KindHolder, nil)
		if err != nil {
			t.Fatal(err)
		}
		remote := <-accepted
		if remote == nil {
			t.Fatal("guest did not accept the holder stream")
		}
		c := &client{conn: conn, w: proto.NewWriter(conn), done: make(chan struct{})}
		clients = append(clients, c)
		relays.Add(1)
		go func() { defer relays.Done(); s.relay(c, st, bufio.NewReader(st)) }()
		return remote
	}
	var slow []*delayedDeadlineConn
	for range 2 {
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = peer.Close() })
		conn := &delayedDeadlineConn{Conn: server, writing: make(chan struct{})}
		slow = append(slow, conn)
		stream := open(conn)
		go func() { _ = proto.NewWriter(stream).Write(proto.TSnapshot, make([]byte, proto.MaxFrame-1)) }()
		select {
		case <-conn.writing:
		case <-time.After(3 * time.Second):
			t.Fatal("slow client never occupied a relay buffer")
		}
	}
	s.relayMu.Lock()
	used := s.relayUsed
	s.relayMu.Unlock()
	if used != 2*(proto.MaxFrame-1) {
		t.Fatalf("stalled clients hold %d relay bytes, want %d", used, 2*(proto.MaxFrame-1))
	}
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = peer.Close() })
	stream := open(server)
	want := bytes.Repeat([]byte{0, 1, 7, 27, 128, 255}, keepBuf/6+20)
	go func() { _ = proto.NewWriter(stream).Write(proto.TSnapshot, want) }()
	for _, c := range slow {
		c.advance()
	}
	_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
	got, err := proto.Read(bufio.NewReader(peer))
	if err != nil || got.Type != proto.TSnapshot || !bytes.Equal(got.Payload, want) {
		t.Fatalf("healthy snapshot: type=%v size=%d err=%v", got.Type, len(got.Payload), err)
	}
}

var errDeadlineInstall = errors.New("cannot install a write deadline")

type noDeadlineConn struct {
	net.Conn
	writes int
	closed bool
}

func (c *noDeadlineConn) SetWriteDeadline(time.Time) error { return errDeadlineInstall }
func (c *noDeadlineConn) Write(p []byte) (int, error) {
	c.writes++
	return len(p), nil
}
func (c *noDeadlineConn) Close() error { c.closed = true; return nil }

func TestClientRefusesAnUnboundedWrite(t *testing.T) {
	conn := &noDeadlineConn{}
	c := &client{conn: conn, w: proto.NewWriter(conn), done: make(chan struct{})}
	err := c.write(proto.TOutput, []byte("output"))
	if !errors.Is(err, errDeadlineInstall) || conn.writes != 0 || !conn.closed {
		t.Fatalf("deadline failure: err=%v writes=%d closed=%v", err, conn.writes, conn.closed)
	}
}

type partialOutputConn struct {
	net.Conn
	writes int
}

func (c *partialOutputConn) SetWriteDeadline(time.Time) error { return nil }
func (c *partialOutputConn) Write(p []byte) (int, error) {
	c.writes++
	return min(3, len(p)), io.ErrUnexpectedEOF
}
func (c *partialOutputConn) Close() error { return nil }

func TestClientNeverAddsAHeaderAfterAPartialWrite(t *testing.T) {
	conn := &partialOutputConn{}
	c := &client{conn: conn, w: proto.NewWriter(conn), done: make(chan struct{}), drain: time.Second}
	t.Cleanup(c.close)
	first := c.write(proto.TOutput, []byte("first"))
	second := c.write(proto.TSnapshot, []byte("second"))
	if !errors.Is(first, io.ErrUnexpectedEOF) || !errors.Is(second, io.ErrUnexpectedEOF) || conn.writes != 1 {
		t.Fatalf("partial write: first=%v second=%v calls=%d", first, second, conn.writes)
	}
}
