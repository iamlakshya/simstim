package link

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// stalled starts a supervisor's link whose peer sends its hello and reads
// nothing. The link's writer then waits on its own hello, so it sends no
// frame until the peer reads.
func stalled(tb testing.TB, opt Options) (*Session, *peer) {
	tb.Helper()
	a, b := net.Pipe()
	tb.Cleanup(func() { a.Close(); b.Close() })
	s := New(a, Supervisor, opt)
	tb.Cleanup(func() { s.Close() })
	p := &peer{tb: tb, conn: b, r: bufio.NewReader(b), w: proto.NewWriter(b)}
	if err := p.w.WriteJSON(proto.THello, helloOf(Guest)); err != nil {
		tb.Fatal(err)
	}
	return s, p
}

// A frame for a stream that this side has opened but whose open it has
// not sent breaks the link: the other end cannot know the stream.
func TestFramesForUnsentOpens(t *testing.T) {
	for name, send := range map[string]func(p *peer, id uint32){
		"data":    func(p *peer, id uint32) { p.data(id, []byte("x")) },
		"credit":  func(p *peer, id uint32) { p.window(id, 1) },
		"a close": func(p *peer, id uint32) { p.close(id, closeBoth) },
	} {
		t.Run(name, func(t *testing.T) {
			s, p := stalled(t, Options{})
			st, err := s.Open(testCtx(t), KindExec, nil)
			if err != nil {
				t.Fatal(err)
			}
			send(p, st.id)
			broken(t, s, "for stream 1, which was never opened")
		})
	}
}

// While the writer sends nothing, opens and closes wait only up to the
// stream limit, because a stream counts until the writer takes its close. When the
// writer runs again, every open and close goes, and every slot is free.
func TestOpenChurnWithAStoppedWriter(t *testing.T) {
	s, p := stalled(t, Options{})
	n := 0
	for {
		st, err := s.Open(testCtx(t), KindExec, nil)
		if err != nil {
			if !strings.Contains(err.Error(), "the supervisor has 32 streams open") {
				t.Fatal(err)
			}
			break
		}
		n++
		_ = st.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
		if err := st.Close(); err == nil || !strings.Contains(err.Error(), "had not reached the connection") {
			t.Fatalf("close %d: %v", n, err)
		}
	}
	s.mu.Lock()
	opens, count := len(s.opens), s.count[Supervisor]
	s.mu.Unlock()
	if n != DefaultMaxSupervisorStreams || opens != n || count != n {
		t.Fatalf("%d opened, %d opens wait, %d streams counted", n, opens, count)
	}
	if f := p.next(); f.Type != proto.THello {
		t.Fatalf("frame %v", f.Type)
	}
	var got []string
	for range 2 * n {
		f := p.next()
		got = append(got, fmt.Sprintf("%#x/%d", byte(f.Type), binary.LittleEndian.Uint32(f.Payload)))
	}
	if got[0] != "0x50/1" || got[n-1] != fmt.Sprintf("0x50/%d", 2*n-1) || got[n] != "0x52/1" || got[2*n-1] != fmt.Sprintf("0x52/%d", 2*n-1) {
		t.Errorf("frames %v", got)
	}
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.streams) == 0 && s.count[Supervisor] == 0 && len(s.opens) == 0
	})
}

// A stream that leaves the link takes its waiting open with it. That
// happens only when the link has ended first.
func TestRemoveTakesAWaitingOpen(t *testing.T) {
	s, _ := stalled(t, Options{})
	st, err := s.Open(testCtx(t), KindExec, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.opens) != 0 || s.count[Supervisor] != 0 {
		t.Errorf("%d opens wait, %d streams counted", len(s.opens), s.count[Supervisor])
	}
}

// Close returns nil only after the connection has written the stream's
// last bytes and its close. While a peer reads nothing, Close fails at its
// deadline. When the connection then fails, Close returns the link's
// error.
func TestCloseWaitsForTheConnection(t *testing.T) {
	setup := func(t *testing.T) (*Session, *Stream, *peer) {
		s, p := gated(t, Options{})
		st, err := s.Open(testCtx(t), KindPort, nil)
		if err != nil {
			t.Fatal(err)
		}
		if f := p.next(); f.Type != proto.TStreamOpen {
			t.Fatalf("frame %v", f.Type)
		}
		// From here the peer reads nothing, so these bytes never reach it.
		if _, err := st.Write(make([]byte, 1000)); err != nil {
			t.Fatal(err)
		}
		return s, st, p
	}
	t.Run("the deadline", func(t *testing.T) {
		_, st, _ := setup(t)
		_ = st.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
		if err := st.Close(); err == nil || !strings.Contains(err.Error(), "had not reached the connection") {
			t.Errorf("close: %v", err)
		}
	})
	t.Run("a failed connection", func(t *testing.T) {
		s, st, p := setup(t)
		closed := make(chan error, 1)
		go func() { closed <- st.Close() }()
		time.Sleep(100 * time.Millisecond)
		p.conn.Close()
		select {
		case err := <-closed:
			// The reader or the writer sees the failure first. Either way,
			// Close returns the link's error.
			if err == nil || !errors.Is(err, s.Err()) {
				t.Errorf("close: %v, link: %v", err, s.Err())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Close still waits after the connection failed")
		}
	})
}

// Bytes that the writer took into a batch, which the connection never
// finished writing, are lost when the link fails. A Close after that
// returns the link's error, not nil.
func TestCloseAfterTheConnectionFailed(t *testing.T) {
	s, p := gated(t, Options{})
	st, err := s.Open(testCtx(t), KindPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamOpen {
		t.Fatalf("frame %v", f.Type)
	}
	if _, err := st.Write(make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	// The writer takes the bytes into a batch, and waits for the peer,
	// which reads no more.
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.out.len() == 0 && st.lastSeq > s.written })
	p.conn.Close()
	<-s.Done()
	if err := st.Close(); err == nil || !errors.Is(err, s.Err()) {
		t.Errorf("close: %v, link: %v", err, s.Err())
	}
}

// readFrames reads n frames of the link, so that the writer can finish
// the batch that holds them.
func readFrames(p *peer, n int) {
	for range n {
		p.next()
	}
}

// credited gives stream st a second window of credit, so that the writer
// can send its next window at once.
func credited(t *testing.T, s *Session, p *peer, st *Stream) {
	t.Helper()
	p.window(st.id, DefaultWindow)
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.sendCred == 2*DefaultWindow })
}

// blockOn writes a window on st, and waits until the writer has taken it
// into a batch. The peer reads none of it yet, so the writer waits on that
// batch, and frames that come after it go into the next one.
func blockOn(t *testing.T, s *Session, st *Stream) {
	t.Helper()
	if _, err := st.Write(make([]byte, DefaultWindow)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.out.len() == 0 })
}

// unwritten checks that the connection has not finished the batch that the
// peer reads from now.
func unwritten(t *testing.T, s *Session) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.written >= s.seq {
		t.Fatalf("batch %d is written already", s.seq)
	}
}

// The other end may answer an open as soon as it has read the open, while
// the connection still writes the rest of its batch. Its frame for that
// stream is no breach. Here a batch holds the open of stream 3, then 64 KiB
// of stream 1, and the peer reads only the open before it answers.
func TestPeerAnswersAnOpenInABatchBeingWritten(t *testing.T) {
	s, p := gated(t, Options{})
	a, err := s.Open(testCtx(t), KindPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	readFrames(p, 1) // the open of stream 1
	credited(t, s, p, a)
	blockOn(t, s, a)
	if _, err := a.Write(make([]byte, DefaultWindow)); err != nil {
		t.Fatal(err)
	}
	b, err := s.Open(testCtx(t), KindExec, nil)
	if err != nil {
		t.Fatal(err)
	}
	readFrames(p, 2) // the first window of stream 1, in two frames
	if f := p.next(); f.Type != proto.TStreamOpen || binary.LittleEndian.Uint32(f.Payload) != b.id {
		t.Fatalf("frame %v %v, want the open of stream %d", f.Type, f.Payload[:4], b.id)
	}
	unwritten(t, s)
	p.window(b.id, 1)
	readFrames(p, 2) // the second window of stream 1
	alive(t, s, p, 2)
}

// The other end may open a stream as soon as it has read the close that
// freed a slot, while the connection still writes the rest of that batch.
// The slot is free for it. Here the guest may hold one stream, and a
// batch holds the close of that stream, then 64 KiB of another.
func TestSlotFreeWhenThePeerSeesTheClose(t *testing.T) {
	s, p := gated(t, Options{MaxGuestStreams: 1})
	port, err := s.Open(testCtx(t), KindPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	readFrames(p, 1) // the open of stream 1
	p.open(2, `{"kind":"egress"}`)
	g, err := s.Accept(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	credited(t, s, p, port)
	blockOn(t, s, port)
	// The close of stream 2 waits first, then the second window of stream 1.
	closed := make(chan error, 1)
	go func() { closed <- g.Close() }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return g.closedLocal })
	if _, err := port.Write(make([]byte, DefaultWindow)); err != nil {
		t.Fatal(err)
	}
	readFrames(p, 2) // the first window of stream 1
	if f := p.next(); f.Type != proto.TStreamClose || binary.LittleEndian.Uint32(f.Payload) != 2 {
		t.Fatalf("frame %v %v, want the close of stream 2", f.Type, f.Payload)
	}
	unwritten(t, s)
	// The close of stream 2 is in a batch that the connection has not
	// finished writing, so Close still waits.
	select {
	case err := <-closed:
		t.Fatalf("Close returned %v before the connection wrote its close", err)
	case <-time.After(50 * time.Millisecond):
	}
	p.open(4, `{"kind":"egress"}`)
	readFrames(p, 2) // the second window of stream 1
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close still waits")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if st, err := s.Accept(ctx); err != nil || st.id != 4 {
		t.Fatalf("accept: %v, %v (refused %d)", st, err, s.Refused())
	}
	if s.Refused() != 0 {
		t.Errorf("refused %d", s.Refused())
	}
}

// A reset, or a refusal, stays when a close both ways comes after it:
// once both frames have arrived, a Read returns the bytes, then
// ErrRefused, never a clean end.
func TestResetStaysAfterAClose(t *testing.T) {
	s := newSession(nil, Supervisor, Options{})
	st := s.newStream(1, KindPort, nil)
	s.add(st)
	s.nextID, s.sentID = 3, 1
	if err := s.onData(append(idPayload(1), "ab"...)); err != nil {
		t.Fatal(err)
	}
	for _, code := range []byte{closeRefused, closeBoth, closeWrite} {
		if err := s.onClose(idPayload(1, code)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := io.ReadAll(st)
	if string(got) != "ab" || !errors.Is(err, ErrRefused) {
		t.Errorf("read %q, then %v", got, err)
	}
}
