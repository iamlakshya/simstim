package link

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type abortRawConn struct {
	writing         chan struct{}
	release, closed chan struct{}
	once            sync.Once
	closes          atomic.Int32
}

func (c *abortRawConn) Read([]byte) (int, error) { <-c.closed; return 0, net.ErrClosed }
func (c *abortRawConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	<-c.release
	return len(p), nil
}
func (c *abortRawConn) Close() error {
	if c.closes.Add(1) == 1 {
		close(c.closed)
	}
	return nil
}
func (*abortRawConn) LocalAddr() net.Addr              { return nil }
func (*abortRawConn) RemoteAddr() net.Addr             { return nil }
func (*abortRawConn) SetDeadline(time.Time) error      { return nil }
func (*abortRawConn) SetReadDeadline(time.Time) error  { return nil }
func (*abortRawConn) SetWriteDeadline(time.Time) error { return nil }

func TestAbortStreamsRevokesBufferedDataWithoutRawCloseOrJoin(t *testing.T) {
	raw := &abortRawConn{writing: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
	s := New(raw, Guest, Options{})
	<-raw.writing // The actual writer is inside an in-flight raw syscall.
	release := sync.OnceFunc(func() { close(raw.release) })
	t.Cleanup(func() { release(); _ = s.Close() })
	s.mu.Lock()
	st := s.newStream(2, KindAgent, nil)
	s.add(st)
	st.in.write([]byte("buffered-before-abort"), int(s.opt.Window))
	s.mu.Unlock()
	returned := make(chan struct{})
	go func() { s.AbortStreams(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("logical abort waited for raw I/O")
	}
	if raw.closes.Load() != 0 {
		t.Fatal("logical abort closed a transport it does not own")
	}
	if _, err := st.Read(make([]byte, 32)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("buffered data survived logical revocation: %v", err)
	}
	if _, err := s.Accept(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("accept survived logical revocation: %v", err)
	}
	if err := s.onOpen(make([]byte, 4)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("a late raw frame admitted work after abort: %v", err)
	}
	if err := st.Flush(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("flush survived logical revocation: %v", err)
	}
	release()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.closes.Load() != 1 {
		t.Fatal("physical close after logical abort was lost or repeated")
	}
}
