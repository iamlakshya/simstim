package link

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Done observations identify a real select wait, so these tests do not
// depend on sleeping long enough for a Flush goroutine to run.
type flushContext struct {
	context.Context
	waits chan struct{}
}

func (c *flushContext) Done() <-chan struct{} {
	select {
	case c.waits <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

type flushCall struct {
	waits <-chan struct{}
	done  <-chan error
}

func runFlush(t *testing.T, st *Stream) (*flushCall, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(testCtx(t))
	observed := &flushContext{Context: ctx, waits: make(chan struct{}, 8)}
	done, finished := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(finished)
		done <- st.Flush(observed)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("Flush did not leave after cancellation")
		}
	})
	return &flushCall{waits: observed.waits, done: done}, cancel
}

func (f *flushCall) waiting(t *testing.T) {
	t.Helper()
	select {
	case <-f.waits:
	case err := <-f.done:
		t.Fatalf("Flush returned before writer completion: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Flush did not reach its wait")
	}
}

func (f *flushCall) result(t *testing.T) error {
	t.Helper()
	select {
	case err := <-f.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Flush did not return after its event")
		return nil
	}
}

func flushStream(t *testing.T, conn net.Conn) (*Session, *Stream) {
	t.Helper()
	s := newSession(conn, Supervisor, Options{Window: 16})
	st := s.newStream(1, KindPort, nil)
	s.add(st)
	s.nextID, s.sentID = 3, 1
	t.Cleanup(func() { _ = s.Close() })
	return s, st
}

func writeFlushBytes(t *testing.T, st *Stream) {
	t.Helper()
	if n, err := st.Write([]byte("grant")); err != nil || n != 5 {
		t.Fatalf("Write = %d, %v", n, err)
	}
}

func TestFlushWaitsForQueueDrainAndFrozenWriterCompletion(t *testing.T) {
	s, st := flushStream(t, nil)
	writeFlushBytes(t, st)
	f, _ := runFlush(t, st)
	f.waiting(t) // Bytes still wait in the stream's queue.
	batch, seq := s.build()
	if len(batch) == 0 {
		t.Fatal("writer built no data")
	}
	f.waiting(t) // Queue is empty, but the connection has not written it.
	if st.wmu.TryLock() {
		st.wmu.Unlock()
		t.Fatal("Flush let another Write change its byte sequence")
	}
	s.wrote(seq)
	if err := f.result(t); err != nil {
		t.Fatal(err)
	}
	if !st.wmu.TryLock() {
		t.Fatal("Flush retained its writer lock after completion")
	}
	st.wmu.Unlock()
	if n, err := st.Write([]byte("later")); n != 5 || err != nil {
		t.Fatalf("Flush did not release later Writes: %d, %v", n, err)
	}
}

func TestFlushAlreadyDequeuedBytesDoNotPassAsWritten(t *testing.T) {
	s, st := flushStream(t, nil)
	writeFlushBytes(t, st)
	_, seq := s.build()
	f, _ := runFlush(t, st)
	f.waiting(t)
	s.wrote(seq)
	if err := f.result(t); err != nil {
		t.Fatal(err)
	}
}

func TestFlushCanCancelWhileItsBytesStillWaitForCredit(t *testing.T) {
	s, st := flushStream(t, nil)
	s.mu.Lock()
	st.sendCred = 0
	s.mu.Unlock()
	writeFlushBytes(t, st)
	f, cancel := runFlush(t, st)
	f.waiting(t)
	cancel()
	if err := f.result(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Flush cancellation = %v", err)
	}
	s.mu.Lock()
	queued, closed := st.out.len(), s.closed
	s.mu.Unlock()
	if queued != 5 || closed {
		t.Fatalf("Flush changed caller-owned queued data or link: bytes=%d closed=%v", queued, closed)
	}
	if !st.wmu.TryLock() {
		t.Fatal("cancelled Flush retained its writer lock")
	}
	st.wmu.Unlock()
	if n, err := st.Write([]byte("later")); n != 5 || err != nil {
		t.Fatalf("cancelled Flush retained its writer lock: %d, %v", n, err)
	}
}

func TestFlushWatermarkIsNotExtendedByAnotherStream(t *testing.T) {
	s, st := flushStream(t, nil)
	writeFlushBytes(t, st)
	_, target := s.build()
	f, _ := runFlush(t, st)
	f.waiting(t) // Its stream is drained, so this watermark is now fixed.
	other := s.newStream(3, KindPut, nil)
	s.mu.Lock()
	s.add(other)
	s.nextID, s.sentID = 5, 3
	s.mu.Unlock()
	writeFlushBytes(t, other)
	_, unrelated := s.build()
	if unrelated <= target {
		t.Fatal("unrelated activity did not advance the link's batch sequence")
	}
	// This is the observable state when the writer has completed the
	// target, then selected another batch before Flush can take s.mu.
	s.wrote(target)
	if err := f.result(t); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written >= unrelated {
		t.Fatal("test accidentally completed the unrelated batch")
	}
}

func TestFlushCancellationDoesNotWaitForAContendedWriter(t *testing.T) {
	s, st := flushStream(t, nil)
	writer := make(chan error, 1)
	go func() { _, err := st.Write(make([]byte, 32)); writer <- err }()
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return st.out.len() == 16 && st.writeQ.ch != nil
	})
	f, cancel := runFlush(t, st)
	f.waiting(t) // A real wait for wmu, which Write still holds.
	cancel()
	if err := f.result(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("contended Flush = %v", err)
	}
	if err := st.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-writer:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("cancelled writer cleanup = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Write did not leave after CloseWrite")
	}
}

func TestFlushChecksClosedRefusedAndLostStateBeforeEmptySuccess(t *testing.T) {
	for _, state := range []string{"local close", "CloseWrite", "peer close", "refused", "dropped", "link failed"} {
		t.Run(state, func(t *testing.T) {
			s, st := flushStream(t, nil)
			want := error(net.ErrClosed)
			s.mu.Lock()
			switch state {
			case "local close":
				st.closedLocal = true
			case "CloseWrite":
				st.writeShut = true
			case "peer close":
				st.peerGone, want = true, ErrPeerClosed
			case "refused":
				st.refused, want = true, ErrRefused
			case "dropped":
				st.dropped, want = true, ErrRefused
			case "link failed":
				s.closed, s.err = true, io.ErrUnexpectedEOF
				close(s.done)
				want = io.ErrUnexpectedEOF
			}
			s.mu.Unlock()
			if err := st.Flush(testCtx(t)); !errors.Is(err, want) {
				t.Fatalf("Flush after %s = %v, want %v", state, err, want)
			}
		})
	}
}

func TestConcurrentCloseWriteWakesAnInflightFlush(t *testing.T) {
	s, st := flushStream(t, nil)
	writeFlushBytes(t, st)
	s.build() // Empty queue with a batch still in flight.
	f, _ := runFlush(t, st)
	f.waiting(t)
	if err := st.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if err := f.result(t); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Flush after concurrent CloseWrite = %v", err)
	}
}

func TestConcurrentCloseWakesAnInflightFlush(t *testing.T) {
	s, st := flushStream(t, nil)
	writeFlushBytes(t, st)
	s.build()
	f, _ := runFlush(t, st)
	f.waiting(t)
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	if err := f.result(t); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Flush after concurrent Close = %v", err)
	}
	_, seq := s.build()
	s.wrote(seq)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not complete after its writer completion")
	}
}

type flushWriteResult struct {
	n   int // -1 means the full write
	err error
}

type flushWriterConn struct {
	net.Conn
	entered chan []byte
	release chan flushWriteResult
	closed  chan struct{}
	once    sync.Once
}

func (c *flushWriterConn) Write(p []byte) (int, error) {
	if len(p) > 4 && proto.Type(p[4]) == proto.THello {
		return len(p), nil
	}
	c.entered <- append([]byte(nil), p...)
	select {
	case r := <-c.release:
		if r.n < 0 {
			r.n = len(p)
		}
		return r.n, r.err
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *flushWriterConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func blockedFlushWriter(t *testing.T) (*Session, *Stream, *flushWriterConn) {
	t.Helper()
	c := &flushWriterConn{entered: make(chan []byte, 1), release: make(chan flushWriteResult, 1), closed: make(chan struct{})}
	s, st := flushStream(t, c)
	writeFlushBytes(t, st)
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.writeLoop() }()
	select {
	case batch := <-c.entered:
		frames := decode(t, batch)
		if len(frames) != 1 || frames[0] != (frameOf{t: proto.TStreamData, id: st.id, size: 5}) || !bytes.HasSuffix(batch, []byte("grant")) {
			t.Fatalf("the underlying writer received different stream bytes: %v", frames)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the link writer did not reach the underlying Write")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.out.len() != 0 || st.lastSeq <= s.written {
		t.Fatal("test did not stop a dequeued, unwritten batch")
	}
	return s, st, c
}

func TestFlushWaitsForTheActualUnderlyingWrite(t *testing.T) {
	_, st, c := blockedFlushWriter(t)
	f, _ := runFlush(t, st)
	f.waiting(t)
	c.release <- flushWriteResult{n: -1}
	if err := f.result(t); err != nil {
		t.Fatal(err)
	}
}

func TestFlushCancellationLeavesLinkOwnershipWithTheCaller(t *testing.T) {
	s, st, c := blockedFlushWriter(t)
	f, cancel := runFlush(t, st)
	f.waiting(t)
	cancel()
	if err := f.result(t); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	select {
	case <-s.Done():
		t.Fatal("Flush secretly closed the caller's link")
	default:
	}
	select {
	case <-c.closed:
		t.Fatal("Flush secretly closed the underlying connection")
	default:
	}
	// This is the H5 caller's required cancellation boundary.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialOrFailedUnderlyingWriteNeverAdvancesCompletion(t *testing.T) {
	failed := errors.New("controlled connection failure")
	for name, result := range map[string]flushWriteResult{
		"short without error": {n: 1},
		"zero without error":  {n: 0},
		"oversized count":     {n: 100},
		"partial with error":  {n: 1, err: failed},
		"full with error":     {n: -1, err: failed},
	} {
		t.Run(name, func(t *testing.T) {
			s, st, c := blockedFlushWriter(t)
			f, _ := runFlush(t, st)
			f.waiting(t)
			c.release <- result
			want := failed
			if result.err == nil {
				want = io.ErrShortWrite
			}
			if err := f.result(t); !errors.Is(err, want) {
				t.Fatalf("Flush after %s = %v, want %v", name, err, want)
			}
			s.mu.Lock()
			written := s.written
			s.mu.Unlock()
			if written != 0 {
				t.Fatalf("a partial/error write advanced completion to %d", written)
			}
			if err := st.Flush(testCtx(t)); !errors.Is(err, want) {
				t.Fatalf("a later empty-queue Flush hid the write failure: %v", err)
			}
		})
	}
}
