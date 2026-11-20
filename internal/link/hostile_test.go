package link

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// peer is a scripted other end that writes the frames a test gives it,
// right or wrong.
type peer struct {
	tb   testing.TB
	conn net.Conn
	r    *bufio.Reader
	w    *proto.Writer
}

// scripted starts a link on side, with a scripted peer at the other end
// that has sent hello and read the link's.
func scripted(tb testing.TB, side Side, opt Options, hello proto.Hello) (*Session, *peer) {
	tb.Helper()
	a, b := socketPair(tb)
	return scriptedOn(tb, a, b, side, opt, hello)
}

// gated is scripted for a supervisor's link over net.Pipe, which has no
// buffer: the link's writer waits for every byte until the peer reads it.
// A peer that stops reading stops the writer.
func gated(tb testing.TB, opt Options) (*Session, *peer) {
	tb.Helper()
	a, b := net.Pipe()
	tb.Cleanup(func() { a.Close(); b.Close() })
	return scriptedOn(tb, a, b, Supervisor, opt, helloOf(Guest))
}

func scriptedOn(tb testing.TB, a, b net.Conn, side Side, opt Options, hello proto.Hello) (*Session, *peer) {
	tb.Helper()
	s := New(a, side, opt)
	tb.Cleanup(func() { s.Close() })
	p := &peer{tb: tb, conn: b, r: bufio.NewReader(b), w: proto.NewWriter(b)}
	if err := p.w.WriteJSON(proto.THello, hello); err != nil {
		tb.Fatal(err)
	}
	f := p.next()
	if f.Type != proto.THello || !bytes.Contains(f.Payload, []byte(`"role":"`+side.String()+`"`)) {
		tb.Fatalf("the link's hello: %v %s", f.Type, f.Payload)
	}
	return s, p
}

// helloOf is the right hello of side.
func helloOf(side Side) proto.Hello {
	return proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: side.String()}
}

func (p *peer) send(t proto.Type, payload []byte) {
	if err := p.w.Write(t, payload); err != nil {
		p.tb.Fatal(err)
	}
}

func idPayload(id uint32, rest ...byte) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, id), rest...)
}

func (p *peer) open(id uint32, header string) {
	p.send(proto.TStreamOpen, append(idPayload(id), header...))
}
func (p *peer) data(id uint32, b []byte)   { p.send(proto.TStreamData, idPayload(id, b...)) }
func (p *peer) close(id uint32, code byte) { p.send(proto.TStreamClose, idPayload(id, code)) }
func (p *peer) window(id, n uint32) {
	p.send(proto.TStreamWindow, binary.LittleEndian.AppendUint32(idPayload(id), n))
}

// next reads the link's next frame.
func (p *peer) next() proto.Frame {
	p.tb.Helper()
	_ = p.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	f, err := proto.Read(p.r)
	if err != nil {
		p.tb.Fatalf("read a frame from the link: %v", err)
	}
	return f
}

// expectClose reads frames until the close of stream id, and returns its
// code.
func (p *peer) expectClose(id uint32) byte {
	p.tb.Helper()
	for {
		f := p.next()
		if f.Type == proto.TStreamClose && binary.LittleEndian.Uint32(f.Payload) == id {
			return f.Payload[4]
		}
	}
}

// broken waits for the link to end, and checks that it ended for a breach
// whose message holds want.
func broken(tb testing.TB, s *Session, want string) {
	tb.Helper()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		tb.Fatalf("the link is still up, want a breach: %s", want)
	}
	err := s.Err()
	if !errors.Is(err, ErrBreach) || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Try:") {
		tb.Fatalf("the link ended with %v, want a breach: %s", err, want)
	}
}

// alive checks that the link still takes a stream from the guest: an
// egress stream with ID id, which carries a byte.
func alive(tb testing.TB, s *Session, p *peer, id uint32) {
	tb.Helper()
	aliveWith(tb, s, p, id, KindEgress)
}

// aliveWith checks that the link still takes a stream of kind k with ID
// id, which carries a byte.
func aliveWith(tb testing.TB, s *Session, p *peer, id uint32, k Kind) {
	tb.Helper()
	p.open(id, `{"kind":"`+string(k)+`"}`)
	p.data(id, []byte("!"))
	st, err := s.Accept(testCtx(tb))
	if err != nil || st.id != id {
		tb.Fatalf("accept stream %d: %v, %v", id, st, err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(st, b); err != nil || b[0] != '!' {
		tb.Fatalf("read stream %d: %q, %v", id, b, err)
	}
}

// Acceptance: a scripted peer breaks each rule of spec 10.1 for a hostile
// peer, and each breach closes the whole link.
func TestHostilePeerBreaksTheLink(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		script     func(p *peer)
	}{
		{"open with an odd ID from the guest", "stream 3 was opened, but the next ID of the guest is 2", func(p *peer) { p.open(3, `{"kind":"egress"}`) }},
		{"open of ID 0", "stream 0 was opened, but the next ID of the guest is 2", func(p *peer) { p.open(0, `{"kind":"egress"}`) }},
		{"open of the same ID twice", "stream 2 was opened, but the next ID of the guest is 4", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.open(2, `{"kind":"egress"}`)
		}},
		{"open that skips an ID", "stream 6 was opened, but the next ID of the guest is 4", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.open(6, `{"kind":"egress"}`)
		}},
		{"open below the last ID", "stream 2 was opened, but the next ID of the guest is 6", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.open(4, `{"kind":"egress"}`)
			p.open(2, `{"kind":"egress"}`)
		}},
		{"refused open, then the same ID", "stream 2 was opened, but the next ID of the guest is 4", func(p *peer) {
			p.open(2, `{"kind":"holder"}`)
			p.open(2, `{"kind":"egress"}`)
		}},
		{"data for a guest ID never opened", "data for stream 8, which was never opened", func(p *peer) { p.data(8, []byte("x")) }},
		{"data for the guest's next ID", "data for stream 4, which was never opened", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.data(4, []byte("x"))
		}},
		{"credit for a supervisor ID never given", "credit for stream 5, which was never opened", func(p *peer) { p.window(5, 1) }},
		{"a frame of another type", "a frame of type 0x10, which a link does not carry", func(p *peer) {
			p.send(proto.TRequest, []byte(`{"id":1,"op":"ls"}`))
		}},
		{"a second hello", "a frame of type 0x01, which a link does not carry", func(p *peer) {
			_ = p.w.WriteJSON(proto.THello, helloOf(Guest))
		}},
		{"close of ID 0", "a close for stream 0, which was never opened", func(p *peer) { p.close(0, 0) }},
		{"data after a close of the writes", "data on stream 2 after the other end closed it", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.close(2, closeWrite)
			p.data(2, []byte("x"))
		}},
		{"data after a close both ways", "data on stream 2 after the other end closed it", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.close(2, closeBoth)
			p.data(2, []byte("x"))
		}},
		{"data beyond the credit in one frame", "17 bytes on stream 2, beyond its credit of 16", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.data(2, make([]byte, 17))
		}},
		{"data beyond the credit in two frames", "7 bytes on stream 2, beyond its credit of 6", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.data(2, make([]byte, 10))
			p.data(2, make([]byte, 7))
		}},
		{"a short open", "an open frame of 3 bytes", func(p *peer) { p.send(proto.TStreamOpen, []byte{2, 0, 0}) }},
		{"a short data frame", "a data frame of 0 bytes", func(p *peer) { p.send(proto.TStreamData, nil) }},
		{"a long close", "a close frame of 6 bytes", func(p *peer) { p.send(proto.TStreamClose, idPayload(2, 0, 0)) }},
		{"an unknown close code", "close code 3 on stream 2", func(p *peer) {
			p.open(2, `{"kind":"egress"}`)
			p.close(2, 3)
		}},
		{"a short credit frame", "a credit frame of 7 bytes", func(p *peer) { p.send(proto.TStreamWindow, idPayload(2, 0, 0, 0)) }},
		{"a frame over the link's limit", "a frame of 65537 bytes, over 65536", func(p *peer) {
			_, _ = p.conn.Write(binary.LittleEndian.AppendUint32(nil, maxFrame+1))
		}},
		{"a frame over the protocol's limit", "a frame of 4194305 bytes, over 65536", func(p *peer) {
			_, _ = p.conn.Write(binary.LittleEndian.AppendUint32(nil, proto.MaxFrame+1))
		}},
		{"an empty frame", "an empty frame", func(p *peer) {
			_, _ = p.conn.Write(binary.LittleEndian.AppendUint32(nil, 0))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, p := scripted(t, Supervisor, Options{Window: 16}, helloOf(Guest))
			tc.script(p)
			broken(t, s, tc.want)
		})
	}
}

// Credit that would pass the largest u32 breaks the link. The test opens a
// stream from the supervisor, so the credit is for one of its IDs.
func TestCreditPastU32(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	st, err := s.Open(testCtx(t), KindExec, map[string]string{"id": "e1"})
	if err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamOpen || string(f.Payload[4:]) != `{"id":"e1","kind":"exec"}` {
		t.Fatalf("the open: %v %q", f.Type, f.Payload)
	}
	// Credit up to the largest u32 is allowed. The open that follows shows
	// that the link took that frame and is still up.
	p.window(st.id, math.MaxUint32-DefaultWindow)
	alive(t, s, p, 2)
	s.mu.Lock()
	cred := st.sendCred
	s.mu.Unlock()
	if cred != math.MaxUint32 {
		t.Fatalf("credit %d", cred)
	}
	if _, err := st.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamData || string(f.Payload[4:]) != "abc" {
		t.Fatalf("the data: %v %q", f.Type, f.Payload)
	}
	p.window(st.id, 4)
	broken(t, s, "would pass 4294967295 bytes")
	if _, err := st.Write([]byte("x")); !errors.Is(err, ErrBreach) {
		t.Errorf("a write after the breach: %v", err)
	}
}

// A breach closes every stream of the link, on both ends' streams.
func TestBreachEndsEveryStream(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	alive(t, s, p, 2)
	st, err := s.Open(testCtx(t), KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.data(10, []byte("x"))
	broken(t, s, "never opened")
	if _, err := st.Read(make([]byte, 1)); !errors.Is(err, ErrBreach) {
		t.Errorf("read: %v", err)
	}
}

// The hello comes first, from the right role, and the supervisor takes
// only a guest of its own version.
func TestHello(t *testing.T) {
	right := helloOf(Guest)
	for _, tc := range []struct {
		name  string
		hello func() []byte
		want  string
	}{
		{"not a hello", func() []byte { return append(idPayload(2), `{"kind":"egress"}`...) }, "not a hello"},
		{"not JSON", func() []byte { return []byte("{") }, "not valid JSON"},
		{"another magic", func() []byte { h := right; h.Magic = "x"; return mustJSON(h) }, "not of protocol 1.x"},
		{"protocol 2", func() []byte { h := right; h.Protocol = "2.0"; return mustJSON(h) }, "not of protocol 1.x"},
		{"the supervisor's role", func() []byte { h := right; h.Role = "supervisor"; return mustJSON(h) }, `role is "supervisor", not "guest"`},
		{"a role with escapes", func() []byte { h := right; h.Role = "\x1b]0;x\a"; return mustJSON(h) }, `role is "??0?x?"`},
		{"too big", func() []byte { h := right; h.Features = []string{strings.Repeat("f", 5000)}; return mustJSON(h) }, "a hello of"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := socketPair(t)
			s := New(a, Supervisor, Options{})
			defer s.Close()
			typ := proto.THello
			if tc.name == "not a hello" {
				typ = proto.TStreamOpen
			}
			if err := proto.NewWriter(b).Write(typ, tc.hello()); err != nil {
				t.Fatal(err)
			}
			broken(t, s, tc.want)
		})
	}
	t.Run("another version", func(t *testing.T) {
		a, b := socketPair(t)
		s := New(a, Supervisor, Options{})
		defer s.Close()
		h := right
		h.Version = "0.4.0\x1b[2J"
		_ = proto.NewWriter(b).WriteJSON(proto.THello, h)
		<-s.Done()
		var ve *VersionError
		if !errors.As(s.Err(), &ve) || ve.Guest != "0.4.0??2J" {
			t.Fatalf("error %v", s.Err())
		}
		want := "the sandbox runs helper 0.4.0??2J, and its supervisor " + version.Version + ". Try: "
		if !strings.HasPrefix(ve.Error(), want) || !strings.HasSuffix(ve.Error(), "Try: stop the session, then resume it") {
			t.Errorf("message %q", ve.Error())
		}
		if _, err := s.Open(testCtx(t), KindCtl, nil); !errors.As(err, &ve) {
			t.Errorf("open after the refusal: %v", err)
		}
	})
	t.Run("the guest takes any version", func(t *testing.T) {
		h := helloOf(Supervisor)
		h.Version = "0.4.0"
		s, p := scripted(t, Guest, Options{}, h)
		p.open(1, `{"kind":"ctl"}`)
		if st, err := s.Accept(testCtx(t)); err != nil || st.Kind() != KindCtl {
			t.Fatalf("accept: %v, %v", st, err)
		}
	})
	t.Run("the other end goes before its hello", func(t *testing.T) {
		a, b := socketPair(t)
		s := New(a, Supervisor, Options{})
		defer s.Close()
		// Take the link's hello first, so that its writer has nothing more
		// to write when the connection ends, even on a pipe with no buffer.
		if f, err := proto.Read(bufio.NewReader(b)); err != nil || f.Type != proto.THello {
			t.Fatalf("the link's hello: %v, %v", f.Type, err)
		}
		_, _ = b.Write([]byte{9, 0})
		b.Close()
		<-s.Done()
		if !errors.Is(s.Err(), ErrLinkClosed) {
			t.Errorf("error %v", s.Err())
		}
	})
}

func mustJSON(h proto.Hello) []byte {
	b, _ := json.Marshal(h)
	return b
}

// A frame for the ID that this side gives next breaks the link: the other
// end has seen no open of it. So does an unknown frame type on the guest's
// side.
func TestFramesForUnsentIDs(t *testing.T) {
	s, p := scripted(t, Supervisor, Options{}, helloOf(Guest))
	st, err := s.Open(testCtx(t), KindExec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f := p.next(); f.Type != proto.TStreamOpen {
		t.Fatalf("frame %v", f.Type)
	}
	p.window(st.id, 1) // stream 1 was opened: allowed
	p.window(st.id+2, 1)
	broken(t, s, "credit for stream 3, which was never opened")

	g, gp := scripted(t, Guest, Options{}, helloOf(Supervisor))
	gp.send(0x99, nil)
	broken(t, g, "a frame of type 0x99")
}

// Options.Opener replaces the table of who opens what, on both ends, as a
// link between the client and a host's bridge needs: there the host side
// opens rport and events streams.
func TestOpenerOption(t *testing.T) {
	table := map[Kind]Side{KindRPort: Supervisor, "events": Supervisor, KindExec: Supervisor}
	sup, guest := pair(t, Options{Opener: table})
	for _, k := range []Kind{KindRPort, "events"} {
		if _, err := sup.Open(testCtx(t), k, nil); err != nil {
			t.Fatalf("open %s: %v", k, err)
		}
		if st, err := guest.Accept(testCtx(t)); err != nil || st.Kind() != k {
			t.Fatalf("accept %s: %v, %v", k, st, err)
		}
	}
	for _, tc := range []struct {
		s *Session
		k Kind
	}{{sup, KindHolder}, {guest, KindRPort}, {guest, KindEgress}} {
		if _, err := tc.s.Open(testCtx(t), tc.k, nil); err == nil || !strings.Contains(err.Error(), "may not open") {
			t.Errorf("the %s opens %s: %v", tc.s.side, tc.k, err)
		}
	}
	g, p := scripted(t, Guest, Options{Opener: table}, helloOf(Supervisor))
	p.open(1, `{"kind":"holder"}`)
	if code := p.expectClose(1); code != closeRefused || g.Refused() != 1 {
		t.Errorf("a holder stream on a link without holders: code %d, refused %d", code, g.Refused())
	}
}

// A link whose other end sends no hello in time ends with ErrNoHello. A
// hello in time stops the clock.
func TestHelloTimeout(t *testing.T) {
	a, _ := socketPair(t)
	s := New(a, Supervisor, Options{HelloTimeout: 50 * time.Millisecond})
	defer s.Close()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the link is still up")
	}
	if err := s.Err(); !errors.Is(err, ErrNoHello) || !strings.Contains(err.Error(), "Try: check that the guest started") {
		t.Errorf("error %v", err)
	}
	sup, guest := pair(t, Options{HelloTimeout: time.Second})
	if _, err := sup.Open(testCtx(t), KindCtl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := guest.Accept(testCtx(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	for _, s := range []*Session{sup, guest} {
		if err := s.Err(); err != nil {
			t.Errorf("%s: %v", s.side, err)
		}
	}
}
