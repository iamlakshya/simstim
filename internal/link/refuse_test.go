package link

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Acceptance: a wrong kind or side is refused, and a breach of one stream
// closes only that stream. Each case refuses one open with code 2, counts
// it, and leaves the link and its other streams up.
func TestRefusals(t *testing.T) {
	pad := `,"pad":"` + strings.Repeat("a", 64) + `"`
	for _, tc := range []struct {
		name   string
		side   Side // of the link under test
		opt    Options
		script func(p *peer) uint32 // returns the ID it expects refused
		took   int                  // the streams that the script opened and the link took
		next   uint32               // a free ID of the peer, for the check after
	}{
		{"a kind that the guest may not open", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `{"kind":"holder"}`); return 2 }, 0, 4},
		{"a kind that nobody opens", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `{"kind":"nope"}`); return 2 }, 0, 4},
		{"no kind", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `{}`); return 2 }, 0, 4},
		{"a kind in capitals", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `{"kind":"EGRESS"}`); return 2 }, 0, 4},
		{"a header that is not JSON", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `{"kind":`); return 2 }, 0, 4},
		{"a header that is not an object", Supervisor, Options{}, func(p *peer) uint32 { p.open(2, `["egress"]`); return 2 }, 0, 4},
		{"a header over MaxHeader", Supervisor, Options{MaxHeader: 64}, func(p *peer) uint32 { p.open(2, `{"kind":"egress"`+pad+`}`); return 2 }, 0, 4},
		{"an open past MaxGuestStreams", Supervisor, Options{MaxGuestStreams: 2}, func(p *peer) uint32 {
			p.open(2, `{"kind":"egress"}`)
			p.open(4, `{"kind":"dns"}`)
			p.open(6, `{"kind":"egress"}`)
			return 6
		}, 2, 8},
		{"a kind that the supervisor may not open", Guest, Options{}, func(p *peer) uint32 { p.open(1, `{"kind":"egress"}`); return 1 }, 0, 3},
		{"a second ctl stream", Guest, Options{}, func(p *peer) uint32 {
			p.open(1, `{"kind":"ctl"}`)
			p.open(3, `{"kind":"ctl"}`)
			return 3
		}, 1, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := scripted(t, tc.side, tc.opt, helloOf(tc.side.other()))
			id := tc.script(p)
			if code := p.expectClose(id); code != closeRefused {
				t.Fatalf("stream %d closed with code %d, want %d", id, code, closeRefused)
			}
			if s.Refused() != 1 {
				t.Errorf("refused %d", s.Refused())
			}
			// The streams that the link took still work. Closing them
			// makes room for the check below.
			for range tc.took {
				st, err := s.Accept(testCtx(t))
				if err != nil {
					t.Fatal(err)
				}
				if err := st.SetReadDeadline(time.Now()); err != nil {
					t.Fatal(err)
				}
				if _, err := st.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
					t.Errorf("read %s: %v", st.LocalAddr(), err)
				}
				closeAndRead(t, st, p)
			}
			waitFor(t, func() bool { return streams(s) == 0 })
			k := KindEgress
			if tc.side == Guest {
				k = KindExec
			}
			aliveWith(t, s, p, tc.next, k)
			select {
			case <-s.Done():
				t.Fatalf("the link ended: %v", s.Err())
			default:
			}
		})
	}
}

// closeAndRead closes st while the peer reads the close frame. Close waits
// until the connection has written that frame, which on a pipe with no
// buffer needs the read at the same time.
func closeAndRead(t *testing.T, st *Stream, p *peer) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- st.Close() }()
	if code := p.expectClose(st.id); code != closeBoth {
		t.Fatalf("stream %d: close code %d", st.id, code)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close %d: %v", st.id, err)
	}
}

// A frame for a stream that the receiver has closed is dropped, because its
// sender may not have seen the close yet. IDs are consecutive, so an ID at
// or below the last one that is not open is such a stream.
func TestFramesAfterACloseAreDropped(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	p.open(2, `{"kind":"egress"}`)
	st, err := s.Accept(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	closeAndRead(t, st, p)
	p.data(2, []byte("late"))
	p.window(2, 5)
	p.close(2, closeWrite)
	// A refused stream's frames are dropped too.
	p.open(4, `{"kind":"holder"}`)
	p.data(4, []byte("x"))
	p.window(4, 1)
	p.close(4, closeBoth)
	alive(t, s, p, 6)
	if streams(s) != 1 {
		t.Errorf("%d streams, want 1", streams(s))
	}
}

// The other end closes both ways while bytes still wait for credit: they
// are dropped, writes fail, and Close sends nothing more.
func TestPeerClosesBothWays(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{Window: 16}, helloOf(Guest))
	st, err := s.Open(testCtx(t), KindPut, nil)
	if err != nil {
		t.Fatal(err)
	}
	writeWhileRead(t, st, p, proto.TStreamOpen, proto.TStreamData)
	p.close(st.id, closeBoth)
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("read: %v", err)
	}
	s.mu.Lock()
	queued, mem := st.out.len(), st.out.cap()
	s.mu.Unlock()
	if queued != 0 || mem != 0 {
		t.Errorf("%d bytes still queued in %d bytes of memory", queued, mem)
	}
	if _, err := st.Write([]byte("x")); !errors.Is(err, ErrPeerClosed) {
		t.Errorf("write: %v", err)
	}
	if streams(s) != 1 {
		t.Errorf("%d streams before Close, want 1", streams(s))
	}
	_ = st.Close()
	if streams(s) != 0 {
		t.Errorf("%d streams after Close", streams(s))
	}
	// The next frame is the next stream's open, not a close of the first.
	if _, err := s.Open(testCtx(t), KindPut, nil); err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamOpen {
		t.Errorf("frame %v %q", f.Type, f.Payload)
	}
}

// The other end refuses a stream: reads and writes fail with ErrRefused.
func TestOpenRefused(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	st, err := s.Open(testCtx(t), KindBundle, map[string]string{"dir": "in", "repo": "app"})
	if err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamOpen {
		t.Fatalf("frame %v", f.Type)
	}
	p.close(st.id, closeRefused)
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, ErrRefused) {
		t.Errorf("read: %v", err)
	}
	if _, err := st.Write([]byte("x")); !errors.Is(err, ErrRefused) {
		t.Errorf("write: %v", err)
	}
	_ = st.Close()
	if streams(s) != 0 {
		t.Errorf("%d streams", streams(s))
	}
}

// When the refusals that wait fill their queue, the reader stops until the
// writer has taken them, and the link goes on. The reader runs here
// without a writer, and the test plays the writer.
func TestRefusalsWaitForTheWriter(t *testing.T) {
	s := newSession(nil, Supervisor, Options{MaxGuestStreams: 4})
	var in bytes.Buffer
	w := proto.NewWriter(&in)
	_ = w.WriteJSON(proto.THello, helloOf(Guest))
	for i := range 6 {
		_ = w.Write(proto.TStreamOpen, append(idPayload(uint32(2+2*i)), `{"kind":"holder"}`...))
	}
	done := make(chan error, 1)
	go func() { done <- s.readFrames(bufio.NewReader(&in)) }()
	waitFor(t, func() bool { return s.Refused() == 4 })
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("the reader went on past a full queue: %v", err)
	default:
	}
	if s.Refused() != 4 {
		t.Fatalf("refused %d with the queue full", s.Refused())
	}
	if got := decode(t, s.batch()); len(got) != 4 {
		t.Fatalf("the writer took %v", got)
	}
	if err := <-done; !errors.Is(err, ErrLinkClosed) {
		t.Fatalf("the reader ended with %v, want the end of its input", err)
	}
	if s.Refused() != 6 {
		t.Errorf("refused %d", s.Refused())
	}
}

// A burst of opens that are all refused, sent at once to a link whose other
// end reads, leaves the link up: every open gets its refusal.
func TestRefusalBurst(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	const n = 2000
	var burst bytes.Buffer
	w := proto.NewWriter(&burst)
	for i := range n {
		_ = w.Write(proto.TStreamOpen, append(idPayload(uint32(2+2*i)), `{"kind":"holder"}`...))
	}
	go func() { _, _ = p.conn.Write(burst.Bytes()) }()
	for i := range n {
		if code := p.expectClose(uint32(2 + 2*i)); code != closeRefused {
			t.Fatalf("stream %d: close code %d", 2+2*i, code)
		}
	}
	alive(t, s, p, 2+2*n)
	if s.Refused() != n {
		t.Errorf("refused %d", s.Refused())
	}
}
