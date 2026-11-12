package link

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

const (
	// maxFrame is the largest frame of a link, its type byte and payload,
	// that either end sends or takes: the cap of spec 5.3 on the frames
	// that the supervisor parses.
	maxFrame = 64 << 10
	// helloMax is the largest hello that a link takes.
	helloMax = 4 << 10
)

// VersionError is the error of a supervisor whose guest init runs another
// version of the helper (spec 6.9).
type VersionError struct {
	Guest      string // the guest's version, cleaned
	Supervisor string
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("the sandbox runs helper %s, and its supervisor %s. Try: stop the session, then resume it", e.Guest, e.Supervisor)
}

// readFrames reads the other end's hello, then its frames, until the link
// ends or the other end breaks a rule. It is the only reader of the
// connection. It never waits for a stream, only for the writer to send
// the refusals that wait.
func (s *Session) readFrames(r *bufio.Reader) error {
	if err := s.readHello(r); err != nil {
		return err
	}
	for {
		s.waitRefusals()
		f, err := s.readFrame(r)
		if err != nil {
			return err
		}
		if err := s.handle(f); err != nil {
			return err
		}
	}
}

// waitRefusals stops the reader while the refusals that wait fill their
// queue, until the writer takes them. The other end then waits too, and
// the link goes on.
func (s *Session) waitRefusals() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.refusals) >= s.opt.maxOpen(s.side.other()) && !s.closed {
		c := s.drained.wait()
		s.mu.Unlock()
		select {
		case <-c:
		case <-s.done:
		}
		s.mu.Lock()
	}
}

// readFrame reads one frame into a buffer that the next frame reuses. It
// refuses an empty frame, and a frame over maxFrame before it reads the
// frame's body.
func (s *Session) readFrame(r *bufio.Reader) (proto.Frame, error) {
	head, err := r.Peek(4)
	if err != nil {
		return proto.Frame{}, readErr(err)
	}
	switch n := binary.LittleEndian.Uint32(head); {
	case n == 0:
		return proto.Frame{}, breach("an empty frame")
	case n > maxFrame:
		return proto.Frame{}, breach("a frame of %d bytes, over %d", n, maxFrame)
	}
	if s.rbuf == nil {
		s.rbuf = make([]byte, maxFrame)
	}
	buf := s.rbuf[:binary.LittleEndian.Uint32(head)]
	_, _ = r.Discard(4)
	if _, err := io.ReadFull(r, buf); err != nil {
		return proto.Frame{}, readErr(err)
	}
	return proto.Frame{Type: proto.Type(buf[0]), Payload: buf[1:]}, nil
}

// readHello checks the other end's hello: the magic, the major protocol,
// the role, and on the supervisor's side the version.
func (s *Session) readHello(r *bufio.Reader) error {
	f, err := s.readFrame(r)
	if err != nil {
		return err
	}
	if f.Type != proto.THello {
		return breach("the first frame is of type %#02x, not a hello", byte(f.Type))
	}
	if len(f.Payload) > helloMax {
		return breach("a hello of %d bytes, over %d", len(f.Payload), helloMax)
	}
	var h proto.Hello
	if err := json.Unmarshal(f.Payload, &h); err != nil {
		return breach("the hello is not valid JSON")
	}
	if err := h.Check(version.Protocol); err != nil {
		return breach("the hello is not of protocol %s.x", version.Protocol[:1])
	}
	want := s.side.other().String()
	if h.Role != want {
		return breach("the hello's role is %q, not %q", clean(h.Role), want)
	}
	if s.side == Supervisor && h.Version != version.Version {
		return &VersionError{Guest: clean(h.Version), Supervisor: version.Version}
	}
	s.mu.Lock()
	s.helloOK = true
	s.mu.Unlock()
	close(s.hello)
	return nil
}

// clean keeps the characters of a role or version from a string that the
// other end sent, and at most 64 of them, so that an error never carries
// its control characters.
func clean(v string) string {
	var b strings.Builder
	for _, r := range v {
		if b.Len() >= 64 {
			b.WriteString("…")
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune(".+-_", r):
			b.WriteRune(r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// readErr is the link's error for an error that reading the connection
// gave.
func readErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrLinkClosed
	}
	return fmt.Errorf("link: read: %w", err)
}

// handle applies one frame of the other end. Both ends run one version of
// the helper, so a frame of another type breaks the link.
func (s *Session) handle(f proto.Frame) error {
	switch f.Type {
	case proto.TStreamOpen:
		return s.onOpen(f.Payload)
	case proto.TStreamData:
		return s.onData(f.Payload)
	case proto.TStreamClose:
		return s.onClose(f.Payload)
	case proto.TStreamWindow:
		return s.onWindow(f.Payload)
	default:
		return breach("a frame of type %#02x, which a link does not carry", byte(f.Type))
	}
}

// onOpen takes a stream that the other end opened, or refuses it. Its ID
// must be the other end's last ID plus 2.
func (s *Session) onOpen(p []byte) error {
	if len(p) < 4 {
		return breach("an open frame of %d bytes", len(p))
	}
	id := binary.LittleEndian.Uint32(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	if uint64(id) != s.nextPeer {
		return breach("stream %d was opened, but the next ID of the %s is %d", id, s.side.other(), s.nextPeer)
	}
	s.nextPeer += 2
	k, ok := s.admit(p[4:])
	if !ok {
		s.refused++
		s.refusals = append(s.refusals, id)
		signal(s.wake)
		return nil
	}
	st := s.newStream(id, k, append([]byte(nil), p[4:]...))
	s.add(st)
	s.accepts = append(s.accepts, st)
	s.acceptQ.wake()
	return nil
}

// admit checks an open's header: its size, the streams that the other end
// has open, its kind, and the side that may open that kind. Under s.mu.
func (s *Session) admit(hdr []byte) (Kind, bool) {
	peer := s.side.other()
	if len(hdr) > s.opt.MaxHeader || s.count[peer] >= s.opt.maxOpen(peer) {
		return "", false
	}
	var h struct {
		Kind Kind `json:"kind"`
	}
	if json.Unmarshal(hdr, &h) != nil {
		return "", false
	}
	if who, ok := s.opt.Opener[h.Kind]; !ok || who != peer {
		return "", false
	}
	if h.Kind == KindCtl {
		if s.ctlOpen {
			return "", false
		}
		s.ctlOpen = true
	}
	return h.Kind, true
}

// lookup finds the stream of a frame from the other end. An ID whose open
// has not passed breaks the link, even for a stream that this side has
// opened and not sent yet. For a stream that was opened and has been
// closed here, it gives nil and no error: the frame is dropped, because its
// sender may not have seen the close yet. Under s.mu.
func (s *Session) lookup(id uint32, what string) (*Stream, error) {
	if s.neverOpened(id) {
		return nil, breach("%s for stream %d, which was never opened", what, id)
	}
	return s.streams[id], nil
}

// neverOpened reports whether no open of id has passed between the ends:
// ID 0, an ID of this side above the last open that it sent, or an ID of
// the other side from its next one on. IDs from each side are
// consecutive, so any other ID was opened. Under s.mu.
func (s *Session) neverOpened(id uint32) bool {
	switch {
	case id == 0:
		return true
	case s.side.owns(id):
		return id > s.sentID
	}
	return uint64(id) >= s.nextPeer
}

// onData copies the bytes of a stream into its buffer, within its credit.
func (s *Session) onData(p []byte) error {
	if len(p) < 4 {
		return breach("a data frame of %d bytes", len(p))
	}
	id, data := binary.LittleEndian.Uint32(p), p[4:]
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.lookup(id, "data")
	if st == nil {
		return err
	}
	if st.remoteEOF {
		return breach("data on stream %d after the other end closed it", id)
	}
	if int64(len(data)) > st.recvWin {
		return breach("%d bytes on stream %d, beyond its credit of %d", len(data), id, st.recvWin)
	}
	st.recvWin -= int64(len(data))
	if st.closedLocal {
		// Nobody reads them. Their credit goes back at once.
		st.consume(len(data))
		return nil
	}
	st.in.write(data, int(s.opt.Window))
	st.readQ.wake()
	return nil
}

// onClose applies the other end's close of a stream.
func (s *Session) onClose(p []byte) error {
	if len(p) != 5 {
		return breach("a close frame of %d bytes", len(p))
	}
	id, code := binary.LittleEndian.Uint32(p), p[4]
	if code > closeRefused {
		return breach("close code %d on stream %d", code, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.lookup(id, "a close")
	if st == nil {
		return err
	}
	st.remoteEOF = true
	if code != closeWrite {
		// The other end reads no more and has forgotten the stream, so
		// bytes not yet sent, and this end's close, would be dropped.
		st.peerGone = true
		// An end never gets weaker: a later close keeps a refusal or a
		// reset.
		st.refused = st.refused || code == closeRefused
		st.dropped = st.dropped || (code == closeRefused && st.out.len() > 0)
		st.out.reset()
		st.closeCode = closeNone
		if st.closedLocal {
			s.remove(st)
		}
	}
	st.readQ.wake()
	st.writeQ.wake()
	return nil
}

// onWindow adds the other end's credit to a stream.
func (s *Session) onWindow(p []byte) error {
	if len(p) != 8 {
		return breach("a credit frame of %d bytes", len(p))
	}
	id, n := binary.LittleEndian.Uint32(p), binary.LittleEndian.Uint32(p[4:])
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.lookup(id, "credit")
	if st == nil {
		return err
	}
	if st.sendCred+int64(n) > math.MaxUint32 {
		return breach("credit on stream %d would pass %d bytes", id, uint32(math.MaxUint32))
	}
	st.sendCred += int64(n)
	s.schedule(st)
	return nil
}
