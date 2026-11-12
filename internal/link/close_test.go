package link

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// openPair opens a port stream from the supervisor and accepts it on the
// guest.
func openPair(tb testing.TB, sup, guest *Session) (*Stream, *Stream) {
	tb.Helper()
	st, err := sup.Open(testCtx(tb), KindPort, nil)
	if err != nil {
		tb.Fatal(err)
	}
	g, err := guest.Accept(testCtx(tb))
	if err != nil {
		tb.Fatal(err)
	}
	return st, g
}

// Close sends the bytes that wait, then the close. The other end reads
// every byte, then io.EOF.
func TestCloseSendsQueuedBytes(t *testing.T) {
	sup, guest := pair(t, Options{})
	st, g := openPair(t, sup, guest)
	want := bytes.Repeat([]byte("0123456789abcdef"), 600<<10/16)
	closing, closed := make(chan struct{}), make(chan error, 1)
	go func() {
		if _, err := st.Write(want); err != nil {
			closed <- err
			return
		}
		close(closing)
		closed <- st.Close()
	}()
	// Write returns once all but two windows have been read: one window
	// waits at this end, and one is in flight.
	got := make([]byte, 520<<10)
	if _, err := io.ReadFull(g, got); err != nil {
		t.Fatal(err)
	}
	// Write has returned, so bytes still wait behind Close.
	<-closing
	time.Sleep(50 * time.Millisecond)
	rest, err := io.ReadAll(g)
	if err != nil || !bytes.Equal(append(got, rest...), want) {
		t.Fatalf("read %d bytes of %d, %v", len(got)+len(rest), len(want), err)
	}
	if err := <-closed; err != nil {
		t.Errorf("close: %v", err)
	}
}

// When the other end takes none of the bytes that wait, Close gives up at
// the write deadline: it resets the stream and says so. The other end
// reads what came, then an error, never a clean end.
func TestCloseResetsWhenNothingIsTaken(t *testing.T) {
	sup, guest := pair(t, Options{})
	st, g := openPair(t, sup, guest)
	if _, err := st.Write(make([]byte, 100<<10)); err != nil {
		t.Fatal(err)
	}
	// The first window has gone, and the rest waits for credit.
	waitFor(t, func() bool { sup.mu.Lock(); defer sup.mu.Unlock(); return st.sendCred == 0 })
	if err := st.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	err := st.Close()
	if err == nil || !strings.Contains(err.Error(), "was reset") || !strings.Contains(err.Error(), "Try:") {
		t.Fatalf("close: %v", err)
	}
	got, err := io.ReadAll(g)
	if !errors.Is(err, ErrRefused) || len(got) != DefaultWindow {
		t.Errorf("read %d bytes, then %v", len(got), err)
	}
	waitFor(t, func() bool { return streams(sup) == 0 })
}

// The other end closes both ways, with no credit, while Close waits on
// bytes that have no credit. The writer cannot end the stream by itself,
// so only the other end's close frees it: the stream leaves the link at
// once, and Close returns.
func TestPeerCloseEndsAWaitingClose(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{Window: 16}, helloOf(Guest))
	st, err := s.Open(testCtx(t), KindPort, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeWhileRead(t, st, p, proto.TStreamOpen, proto.TStreamData)
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.closedLocal })
	p.close(st.id, closeBoth)
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close still waits after the other end closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.streams) != 0 || s.count[Supervisor] != 0 {
		t.Errorf("%d streams left", len(s.streams))
	}
}

// Bytes that arrive after Close are dropped, and their credit goes back
// at once.
func TestDataAfterCloseIsDropped(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{Window: 16}, helloOf(Guest))
	p.open(2, `{"kind":"egress"}`)
	st, err := s.Accept(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	writeWhileRead(t, st, p, proto.TStreamData)
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.closedLocal })
	p.data(2, make([]byte, 16))
	if f := p.next(); f.Type != proto.TStreamWindow || binary.LittleEndian.Uint32(f.Payload[4:]) != 16 {
		t.Fatalf("frame %v %v, want credit for 16 bytes", f.Type, f.Payload)
	}
	s.mu.Lock()
	held, mem := st.in.len(), st.in.cap()
	s.mu.Unlock()
	if held != 0 || mem != 0 {
		t.Errorf("%d bytes held in %d bytes of memory after Close", held, mem)
	}
	p.close(2, closeBoth)
	if err := <-closed; err != nil {
		t.Errorf("close: %v", err)
	}
}

// The other end resets the stream, while Close waits on bytes that have
// no credit or before Close: those bytes are lost, so Close returns
// ErrRefused, not nil.
func TestResetMakesCloseFail(t *testing.T) {
	for name, waiting := range map[string]bool{"while Close waits": true, "before Close": false} {
		t.Run(name, func(t *testing.T) {
			s, p := scripted(t, Supervisor, Options{Window: 16}, helloOf(Guest))
			st, err := s.Open(testCtx(t), KindPort, nil)
			if err != nil {
				t.Fatal(err)
			}
			writeWhileRead(t, st, p, proto.TStreamOpen, proto.TStreamData)
			closed := make(chan error, 1)
			if waiting {
				go func() { closed <- st.Close() }()
				waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.closedLocal })
				p.close(st.id, closeRefused)
			} else {
				p.close(st.id, closeRefused)
				waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return st.peerGone })
				go func() { closed <- st.Close() }()
			}
			select {
			case err := <-closed:
				if !errors.Is(err, ErrRefused) {
					t.Errorf("close: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Close still waits after the reset")
			}
			if streams(s) != 0 {
				t.Errorf("%d streams left", streams(s))
			}
		})
	}
}

// writeWhileRead writes 32 bytes on st while the peer reads frames of the
// types want, which the writer sends meanwhile: a pipe with no buffer
// stops the writer until they are read. With a window of 16, that gives
// 16 bytes in one data frame, and 16 that wait for credit.
func writeWhileRead(t *testing.T, st *Stream, p *peer, want ...proto.Type) {
	t.Helper()
	wrote := make(chan error, 1)
	go func() {
		_, err := st.Write(make([]byte, 32))
		wrote <- err
	}()
	for _, w := range want {
		f := p.next()
		if f.Type != w || (w == proto.TStreamData && len(f.Payload) != 4+16) {
			t.Fatalf("frame %v of %d bytes, want %v", f.Type, len(f.Payload), w)
		}
	}
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
}

// waiters starts n calls of f, waits until they wait, then runs event and
// returns what each call returned.
func waiters(t *testing.T, n int, f func() error, event func()) []error {
	t.Helper()
	errs := make(chan error, n)
	for range n {
		go func() { errs <- f() }()
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-errs:
		t.Fatalf("a call returned before the event: %v", err)
	default:
	}
	event()
	out := make([]error, 0, n)
	for range n {
		select {
		case err := <-errs:
			out = append(out, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("%d of %d calls still wait after the event", n-len(out), n)
		}
	}
	return out
}

// Every Read that waits on a stream wakes for data, the other end's close,
// Close and a new deadline. So does every Write that waits.
func TestEveryWaiterWakes(t *testing.T) {
	read := func(st *Stream) func() error {
		return func() error {
			_, err := st.Read(make([]byte, 1))
			return err
		}
	}
	check := func(name string, errs []error, ok func(error) bool) {
		for _, err := range errs {
			if !ok(err) {
				t.Errorf("%s: %v", name, err)
			}
		}
	}
	sup, guest := pair(t, Options{Window: 16})

	st, g := openPair(t, sup, guest)
	check("data", waiters(t, 3, read(st), func() { _, _ = g.Write([]byte("abc")) }), func(err error) bool { return err == nil })
	check("the other end's close", waiters(t, 3, read(st), func() { _ = g.CloseWrite() }), func(err error) bool { return errors.Is(err, io.EOF) })

	st, _ = openPair(t, sup, guest)
	check("Close", waiters(t, 3, read(st), func() { _ = st.Close() }), func(err error) bool { return errors.Is(err, net.ErrClosed) })

	st, _ = openPair(t, sup, guest)
	past := func() { _ = st.SetReadDeadline(time.Now().Add(-time.Second)) }
	check("a deadline", waiters(t, 3, read(st), past), func(err error) bool { return errors.Is(err, os.ErrDeadlineExceeded) })

	// 16 bytes of credit and 16 queued: every further Write waits.
	st, _ = openPair(t, sup, guest)
	write := func() error {
		_, err := st.Write(make([]byte, 64))
		return err
	}
	check("writes and CloseWrite", waiters(t, 2, write, func() { _ = st.CloseWrite() }), func(err error) bool { return errors.Is(err, net.ErrClosed) })
}

// Two Accepts that wait at once both get a stream.
func TestTwoAcceptsWake(t *testing.T) {
	sup, guest := pair(t, Options{})
	got := make(chan *Stream, 2)
	for range 2 {
		go func() {
			st, _ := sup.Accept(testCtx(t))
			got <- st
		}()
	}
	time.Sleep(50 * time.Millisecond)
	for range 2 {
		if _, err := guest.Open(testCtx(t), KindEgress, nil); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case st := <-got:
			if st == nil || st.Kind() != KindEgress {
				t.Errorf("accepted %v", st)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an Accept still waits")
		}
	}
}

// After Close, or on a link that has ended, CloseWrite and the deadlines
// fail, as on a closed connection.
func TestErrorsAfterClose(t *testing.T) {
	sup, guest := pair(t, Options{})
	st, _ := openPair(t, sup, guest)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"Close":            st.Close(),
		"CloseWrite":       st.CloseWrite(),
		"SetDeadline":      st.SetDeadline(time.Now()),
		"SetReadDeadline":  st.SetReadDeadline(time.Now()),
		"SetWriteDeadline": st.SetWriteDeadline(time.Now()),
	} {
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("%s after Close: %v", name, err)
		}
	}
	st, _ = openPair(t, sup, guest)
	_ = guest.Close()
	<-sup.Done()
	if err := st.CloseWrite(); !errors.Is(err, ErrLinkClosed) {
		t.Errorf("CloseWrite on a dead link: %v", err)
	}
	if err := st.SetDeadline(time.Now()); !errors.Is(err, ErrLinkClosed) {
		t.Errorf("SetDeadline on a dead link: %v", err)
	}
}
