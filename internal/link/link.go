// Package link carries many streams over one byte stream, between a
// supervisor and its guest init. Frames are proto frames of the types
// TStreamOpen, TStreamData, TStreamClose and TStreamWindow, after one
// THello each way.
//
// The trusted side parses a hostile peer's frames, and the other end can
// make it hold only a fixed number of bytes. Every frame has a size limit.
// Each stream has a credit window each way, and holds at most a window of
// bytes each way, whatever the sizes of its frames. Each side has a limit
// on the streams that it opened. A peer that breaks a rule of the protocol
// loses the whole link (ErrBreach). A stream that the peer may not open is
// refused alone, and the link goes on.
package link

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

// Kind is what a stream carries.
type Kind string

// The kinds of stream.
const (
	KindCtl    Kind = "ctl"    // one per link: start, stop, status, alerts
	KindHolder Kind = "holder" // one connection to the holder's socket
	KindEgress Kind = "egress" // one network flow out of the guest
	KindDNS    Kind = "dns"    // one DNS query out of the guest
	KindPort   Kind = "port"   // one forwarded connection into the guest
	KindPut    Kind = "put"    // one pasted file into the guest
	KindBundle Kind = "bundle" // a git bundle in (seed) or out (pull)
	KindExec   Kind = "exec"   // the output and exit code of one exec
	KindRPort  Kind = "rport"  // one connection from the guest to a laptop port
	KindAgent  Kind = "agent"  // one SSH agent connection from the guest
	KindSFTP   Kind = "sftp"   // one SFTP session of an editor
	KindGet    Kind = "get"    // one file copied out of the guest
	KindLayer  Kind = "layer"  // the guest's root as a tar stream, for image save
)

// Side is the end of a link.
type Side int

// The two ends of a link.
const (
	Supervisor Side = iota
	Guest
)

// String is the side's role in its hello.
func (d Side) String() string {
	if d == Supervisor {
		return "supervisor"
	}
	return "guest"
}

// other is the side at the far end.
func (d Side) other() Side {
	if d == Supervisor {
		return Guest
	}
	return Supervisor
}

// owns reports whether the side gives the stream ID id: the supervisor
// gives odd IDs and the guest even ones, so both can open at once.
func (d Side) owns(id uint32) bool { return (id%2 == 1) == (d == Supervisor) }

// first is the first stream ID that the side gives.
func (d Side) first() uint64 {
	if d == Supervisor {
		return 1
	}
	return 2
}

// ownerOf is the side that gave the stream ID id.
func ownerOf(id uint32) Side {
	if Supervisor.owns(id) {
		return Supervisor
	}
	return Guest
}

// Opener says which side may open each kind, on a link between a
// supervisor and its guest init. The other side refuses a stream of another
// kind, or one from the wrong side, and counts it.
var Opener = map[Kind]Side{
	KindCtl: Supervisor, KindHolder: Supervisor, KindPort: Supervisor,
	KindPut: Supervisor, KindBundle: Supervisor, KindExec: Supervisor,
	KindSFTP: Supervisor, KindGet: Supervisor, KindLayer: Supervisor,
	KindEgress: Guest, KindDNS: Guest, KindRPort: Guest, KindAgent: Guest,
}

// The defaults of Options. With them, a link holds at most 8 MiB that the
// other end sent and 8 MiB queued to send: 128 streams of 64 KiB each way.
// They are the project's choice, to keep the supervisor within its budget
// (spec 5.2 and 20.10).
const (
	// DefaultWindow is the credit of each stream, each way.
	DefaultWindow = 64 << 10
	// DefaultMaxGuestStreams is the most streams that the guest opened,
	// open at once.
	DefaultMaxGuestStreams = 96
	// DefaultMaxSupervisorStreams is the most streams that the supervisor
	// opened, open at once. The guest's streams never take these.
	DefaultMaxSupervisorStreams = 32
	// DefaultMaxHeader is the largest JSON header of an open.
	DefaultMaxHeader = 4 << 10
	// DefaultHelloTimeout is how long a link waits for the other end's
	// hello.
	DefaultHelloTimeout = 10 * time.Second
)

// Options bound a link. Both ends must use the same Window, because each
// end starts every stream with that credit for the other. A zero field
// takes its default.
type Options struct {
	Window               uint32 // credit for each stream, each way
	MaxSupervisorStreams int    // streams that the supervisor opened, open at once
	MaxGuestStreams      int    // streams that the guest opened, open at once
	MaxHeader            int    // the JSON header of an open
	// HelloTimeout ends a link whose other end sends no hello in time.
	HelloTimeout time.Duration
	// Opener replaces the package's Opener table, for a link that carries
	// other kinds, such as one between the client and a host's bridge.
	Opener map[Kind]Side
}

func (o Options) withDefaults() Options {
	if o.Window == 0 {
		o.Window = DefaultWindow
	}
	if o.MaxSupervisorStreams <= 0 {
		o.MaxSupervisorStreams = DefaultMaxSupervisorStreams
	}
	if o.MaxGuestStreams <= 0 {
		o.MaxGuestStreams = DefaultMaxGuestStreams
	}
	if o.MaxHeader <= 0 {
		o.MaxHeader = DefaultMaxHeader
	}
	// An open's frame holds the header, the stream ID and the type byte.
	o.MaxHeader = min(o.MaxHeader, maxFrame-1-4)
	if o.HelloTimeout <= 0 {
		o.HelloTimeout = DefaultHelloTimeout
	}
	if o.Opener == nil {
		o.Opener = Opener
	}
	return o
}

// maxOpen is the most streams that side d opened, open at once.
func (o Options) maxOpen(d Side) int {
	if d == Supervisor {
		return o.MaxSupervisorStreams
	}
	return o.MaxGuestStreams
}

var (
	// ErrBreach is wrapped by the error of a link that closed because the
	// other end broke a rule of the protocol.
	ErrBreach = errors.New("link: the other end broke the protocol")
	// ErrLinkClosed is the error of a link whose other end closed it.
	ErrLinkClosed = errors.New("link: the other end closed the link")
	// ErrNoHello is wrapped by the error of a link whose other end sent no
	// hello within Options.HelloTimeout.
	ErrNoHello = errors.New("link: the other end sent no hello in time")
	// ErrRefused is the error of a stream that the other end refused, or
	// reset because it could not send its last bytes.
	ErrRefused = errors.New("link: the other end refused or reset the stream")
	// ErrPeerClosed is the error of a write to a stream that the other end
	// closed both ways.
	ErrPeerClosed = errors.New("link: the other end closed the stream")
)

// breach is the error that closes a link whose other end broke a rule.
func breach(format string, a ...any) error {
	return fmt.Errorf("%w: %s. Try: stop the session, then resume it", ErrBreach, fmt.Sprintf(format, a...))
}

// Session is one end of a link.
type Session struct {
	conn net.Conn
	side Side
	opt  Options

	hello chan struct{} // closed when the other end's hello has passed
	done  chan struct{} // closed when the link has ended
	wake  chan struct{} // tells the writer that frames wait
	wg    sync.WaitGroup
	rbuf  []byte // the reader's frame, reused

	mu       sync.Mutex
	closed   bool
	err      error
	helloOK  bool
	streams  map[uint32]*Stream
	count    [2]int // open streams, by the side that opened them
	nextID   uint64 // the next ID that this side gives
	sentID   uint32 // the last ID whose open this side has sent
	nextPeer uint64 // the ID that the other side must give next
	ctlOpen  bool   // the link's ctl stream has been opened
	accepts  []*Stream
	acceptQ  waitq
	opens    []*Stream // streams whose open waits, in the order of their IDs
	grants   []*Stream // streams with credit to give
	refusals []uint32  // streams to refuse
	drained  waitq     // the writer has taken the refusals
	hi, lo   []*Stream // streams with frames to send, ctl and holder first
	refused  uint64
	wbuf     []byte // the writer's batch, reused
	seq      uint64 // the number of the last batch built
	written  uint64 // the number of the last batch that the connection took
	progress waitq  // a stream left the link, or a batch was written
}

// New starts a link over conn. It sends this side's hello at once. Streams
// wait for the other side's hello, and the link ends when it does not come
// within Options.HelloTimeout.
func New(conn net.Conn, side Side, opt Options) *Session {
	s := newSession(conn, side, opt)
	timer := time.AfterFunc(s.opt.HelloTimeout, s.noHello)
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		s.fail(s.readFrames(bufio.NewReaderSize(conn, 64<<10)))
		timer.Stop()
	}()
	go func() {
		defer s.wg.Done()
		s.writeLoop()
	}()
	return s
}

func newSession(conn net.Conn, side Side, opt Options) *Session {
	return &Session{
		conn: conn, side: side, opt: opt.withDefaults(),
		hello: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1),
		streams: map[uint32]*Stream{}, nextID: side.first(), nextPeer: side.other().first(),
	}
}

// noHello ends a link whose other end has sent no hello.
func (s *Session) noHello() {
	s.mu.Lock()
	ok := s.helloOK
	s.mu.Unlock()
	if !ok {
		s.fail(fmt.Errorf("%w (%v). Try: check that the %s started", ErrNoHello, s.opt.HelloTimeout, s.side.other()))
	}
}

// Open opens a stream of kind k with a JSON header. The header is a struct
// or a map that marshals to a JSON object, or nil. The open carries it with
// the field "kind" added. Open waits for the other side's hello.
func (s *Session) Open(ctx context.Context, k Kind, header any) (*Stream, error) {
	if who, ok := s.opt.Opener[k]; !ok || who != s.side {
		return nil, fmt.Errorf("link: the %s may not open a %q stream. Try: open it from the %s", s.side, k, s.side.other())
	}
	hdr, err := encodeHeader(k, header)
	if err != nil {
		return nil, err
	}
	if len(hdr) > s.opt.MaxHeader {
		return nil, fmt.Errorf("link: the header of a %s stream has %d bytes, more than the %d that the link takes. Try: send the data on the stream instead", k, len(hdr), s.opt.MaxHeader)
	}
	select {
	case <-s.hello:
	case <-s.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch limit := s.opt.maxOpen(s.side); {
	case s.closed:
		return nil, s.err
	case k == KindCtl && s.ctlOpen:
		return nil, errors.New("link: this link already has its ctl stream. Try: use that stream")
	case s.count[s.side] >= limit:
		return nil, fmt.Errorf("link: the %s has %d streams open, the most that the link takes. Try: close a stream first", s.side, limit)
	case s.nextID > math.MaxUint32:
		return nil, errors.New("link: this link has used all its stream IDs. Try: stop the session, then resume it")
	}
	id := uint32(s.nextID)
	s.nextID += 2
	s.ctlOpen = s.ctlOpen || k == KindCtl
	st := s.newStream(id, k, hdr)
	s.add(st)
	s.opens = append(s.opens, st)
	signal(s.wake)
	return st, nil
}

// encodeHeader adds the field "kind" to the JSON object of header.
func encodeHeader(k Kind, header any) ([]byte, error) {
	var fields map[string]json.RawMessage
	if header != nil {
		b, err := json.Marshal(header)
		if err != nil {
			return nil, fmt.Errorf("link: encode the header of a %s stream: %w. Try: pass a struct or a map of JSON values", k, err)
		}
		// A nil map or pointer marshals to null, and leaves fields nil.
		if err := json.Unmarshal(b, &fields); err != nil {
			return nil, fmt.Errorf("link: the header of a %s stream is not a JSON object. Try: pass a struct or a map", k)
		}
	}
	if _, ok := fields["kind"]; ok {
		return nil, fmt.Errorf("link: the header of a %s stream has its own field \"kind\". Try: leave it out, because Open adds it", k)
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	kind, _ := json.Marshal(k) // a string always marshals
	fields["kind"] = kind
	b, _ := json.Marshal(fields) // raw JSON values that json made
	return b, nil
}

// add counts a new stream. Under s.mu.
func (s *Session) add(st *Stream) {
	s.streams[st.id] = st
	s.count[ownerOf(st.id)]++
}

// Accept waits for a stream that the other side opened.
func (s *Session) Accept(ctx context.Context) (*Stream, error) {
	for {
		s.mu.Lock()
		if s.closed {
			err := s.err
			s.mu.Unlock()
			return nil, err
		}
		if len(s.accepts) > 0 {
			st := s.accepts[0]
			s.accepts[0] = nil
			s.accepts = s.accepts[1:]
			s.mu.Unlock()
			return st, nil
		}
		c := s.acceptQ.wait()
		s.mu.Unlock()
		select {
		case <-c:
		case <-s.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Close ends every stream and the link, and waits for the link's
// goroutines to end.
func (s *Session) Close() error {
	s.fail(net.ErrClosed)
	s.wg.Wait()
	return nil
}

// Done is closed when the link has ended.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the link ended: net.ErrClosed after Close, ErrLinkClosed when
// the other end closed it, an error that wraps ErrNoHello or ErrBreach, a
// *VersionError, or the connection's error. It is nil while the link is up.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Refused counts the streams that the other side opened and this side
// refused.
func (s *Session) Refused() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refused
}

// fail ends the link with err, once.
func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed, s.err = true, err
	close(s.done)
	s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

// signal wakes the one goroutine that waits on c, if any, without
// blocking.
func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}
