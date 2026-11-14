package link

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type abortJoinConn struct {
	read, write, closing, releaseRead, releaseWrite chan struct{}
	readOnce, writeOnce, closeOnce                  sync.Once
	closes, writes                                  atomic.Int32
}

func (c *abortJoinConn) Read([]byte) (int, error) {
	c.readOnce.Do(func() { close(c.read) })
	<-c.closing
	<-c.releaseRead
	return 0, net.ErrClosed
}
func (c *abortJoinConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	c.writeOnce.Do(func() { close(c.write) })
	<-c.closing
	<-c.releaseWrite
	return 0, net.ErrClosed
}
func (c *abortJoinConn) Close() error {
	c.closes.Add(1)
	c.closeOnce.Do(func() { close(c.closing) })
	return nil
}
func (*abortJoinConn) LocalAddr() net.Addr              { return nil }
func (*abortJoinConn) RemoteAddr() net.Addr             { return nil }
func (*abortJoinConn) SetDeadline(time.Time) error      { return nil }
func (*abortJoinConn) SetReadDeadline(time.Time) error  { return nil }
func (*abortJoinConn) SetWriteDeadline(time.Time) error { return nil }

func abortWait(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("owned fixture did not reach " + what)
	}
}

func TestAbortThenClosePhysicallyClosesOnceAndJoinsHeldRawIO(t *testing.T) {
	raw := &abortJoinConn{read: make(chan struct{}), write: make(chan struct{}), closing: make(chan struct{}), releaseRead: make(chan struct{}), releaseWrite: make(chan struct{})}
	s := New(raw, Guest, Options{})
	releaseRead := sync.OnceFunc(func() { close(raw.releaseRead) })
	releaseWrite := sync.OnceFunc(func() { close(raw.releaseWrite) })
	t.Cleanup(func() { releaseRead(); releaseWrite(); _ = s.Close() })
	abortWait(t, "raw read", raw.read)
	abortWait(t, "raw write", raw.write)
	s.mu.Lock()
	st := s.newStream(2, KindAgent, nil)
	s.add(st)
	st.out.write([]byte("must-not-dispatch-after-abort"), int(s.opt.Window))
	s.schedule(st)
	s.mu.Unlock()
	s.AbortStreams()
	if raw.closes.Load() != 0 {
		t.Fatal("logical abort physically closed raw I/O")
	}
	closed := make(chan struct{})
	go func() { _ = s.Close(); close(closed) }()
	abortWait(t, "physical transport close", raw.closing)
	select {
	case <-closed:
		t.Fatal("Close returned before actual raw I/O joined")
	default:
	}
	releaseRead()
	select {
	case <-closed:
		t.Fatal("Close returned before held writer joined")
	default:
	}
	releaseWrite()
	abortWait(t, "raw I/O joins", closed)
	if raw.closes.Load() != 1 || raw.writes.Load() != 1 {
		t.Fatal("physical close repeated or queued bytes escaped revocation", raw.closes.Load(), raw.writes.Load())
	}
	_ = s.Close()
	if raw.closes.Load() != 1 {
		t.Fatal("second Close repeated physical ownership")
	}
}

type abortBodyConn struct {
	hello, header, body      []byte
	entered, release, closed chan struct{}
	once, closeOnce          sync.Once
	mu                       sync.Mutex
	held                     []byte
}

func (c *abortBodyConn) Read(p []byte) (int, error) {
	if len(c.hello) > 0 {
		n := copy(p, c.hello)
		c.hello = c.hello[n:]
		return n, nil
	}
	if len(c.header) > 0 {
		n := copy(p, c.header)
		c.header = c.header[n:]
		return n, nil
	}
	c.mu.Lock()
	c.held = p
	c.mu.Unlock()
	c.once.Do(func() { close(c.entered) })
	<-c.release
	if len(c.body) == 0 {
		<-c.closed
		return 0, net.ErrClosed
	}
	n := copy(p, c.body)
	c.body = c.body[n:]
	return n, nil
}
func (*abortBodyConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *abortBodyConn) Close() error                   { c.closeOnce.Do(func() { close(c.closed) }); return nil }
func (*abortBodyConn) LocalAddr() net.Addr              { return nil }
func (*abortBodyConn) RemoteAddr() net.Addr             { return nil }
func (*abortBodyConn) SetDeadline(time.Time) error      { return nil }
func (*abortBodyConn) SetReadDeadline(time.Time) error  { return nil }
func (*abortBodyConn) SetWriteDeadline(time.Time) error { return nil }

func TestAbortRetainsHeldRawReadBufferAndDropsItsLateFrame(t *testing.T) {
	var hello bytes.Buffer
	if err := proto.NewWriter(&hello).WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{'x'}, maxFrame)
	body[0] = byte(proto.TStreamOpen)
	binary.LittleEndian.PutUint32(body[1:5], 2)
	header := binary.LittleEndian.AppendUint32(nil, uint32(len(body)))
	raw := &abortBodyConn{hello: hello.Bytes(), header: header, body: body, entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	s := New(raw, Supervisor, Options{})
	release := sync.OnceFunc(func() { close(raw.release) })
	t.Cleanup(func() { release(); _ = s.Close() })
	abortWait(t, "raw frame body", raw.entered)
	raw.mu.Lock()
	held := raw.held
	raw.mu.Unlock()
	s.mu.Lock()
	if len(held) != maxFrame || len(s.rbuf) != maxFrame || &held[0] != &s.rbuf[0] {
		s.mu.Unlock()
		t.Fatal("fixture did not hold the actual reusable raw frame buffer")
	}
	before := bytes.Clone(held)
	s.mu.Unlock()
	s.AbortStreams()
	s.mu.Lock()
	if len(s.rbuf) != maxFrame || &s.rbuf[0] != &held[0] || !bytes.Equal(held, before) {
		s.mu.Unlock()
		t.Fatal("logical abort released or mutated an in-flight raw buffer")
	}
	s.mu.Unlock()
	release()
	_ = s.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) != 0 || len(s.accepts) != 0 || s.refused != 0 || s.nextPeer != 2 {
		t.Fatal("a raw frame finishing after abort dispatched or admitted work")
	}
}

type abortHeader struct{ called *atomic.Bool }

func (h abortHeader) MarshalJSON() ([]byte, error) { h.called.Store(true); return []byte(`{}`), nil }

func TestAbortRefusesLateOpenFramesAndCallerHeldGoneStream(t *testing.T) {
	s := newSession(nil, Supervisor, Options{})
	s.mu.Lock()
	st := s.newStream(1, KindCtl, nil)
	s.add(st)
	s.remove(st)
	st.in.write([]byte("caller-held-buffer"), int(s.opt.Window))
	s.mu.Unlock()
	s.AbortStreams()
	var called atomic.Bool
	if _, err := s.Open(context.Background(), KindCtl, abortHeader{&called}); !errors.Is(err, net.ErrClosed) || called.Load() {
		t.Fatal("late Open ran callbacks or admitted after abort", err)
	}
	for _, handle := range []func([]byte) error{s.onOpen, s.onData, s.onClose, s.onWindow} {
		if err := handle(nil); !errors.Is(err, net.ErrClosed) {
			t.Fatal("late malformed frame bypassed terminal state", err)
		}
	}
	if err := s.handle(proto.Frame{Type: proto.TPing}); !errors.Is(err, net.ErrClosed) {
		t.Fatal("late frame dispatched after abort", err)
	}
	if _, err := st.Read(make([]byte, 64)); !errors.Is(err, net.ErrClosed) {
		t.Fatal("a caller-held removed stream exposed buffered bytes", err)
	}
	if err := st.Flush(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("a caller-held removed stream retained write authority", err)
	}
	if _, err := s.Accept(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("late Accept survived abort", err)
	}
	if _, err := s.readFrame(bufio.NewReader(bytes.NewReader(nil))); !errors.Is(err, net.ErrClosed) {
		t.Fatal("a fresh raw read entered after abort", err)
	}
	if b, _ := s.build(); len(b) != 0 {
		t.Fatal("queued frames survived abort")
	}
}
