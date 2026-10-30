package host

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// This file runs the shadow terminal: a child process, built from vt/, that
// keeps a copy of the session's screen with libghostty-vt. A new client gets
// a snapshot from it instead of a replay that may start mid-stream. The
// child is a separate process, so a crash there cannot take the agent down;
// the VT restarts it and feeds it the session's recent output again.
//
// The frame format is in vt/README.md: a type byte, a little-endian uint32
// payload length, then the payload.

// Shadow terminal frame types.
const (
	// Holder to shadow terminal.
	vtFrameData     = 'D'
	vtFrameResize   = 'R'
	vtFrameSnapshot = 'S'
	vtFrameQueries  = 'Q'
	vtFrameProfile  = 'P'
	vtFrameDump     = 'T'
	vtFrameSync     = 'Y'
	// Shadow terminal to holder. S, T and Y reply with their own type.
	vtFrameWrite = 'W'
	vtFrameEvent = 'E'
)

const (
	// vtMaxBehind is how many bytes may wait for the child before it is
	// dropped and rebuilt from the history. It is twice the ring.
	vtMaxBehind = 2 * RingSize
	// vtChunk is the largest data frame sent to the child.
	vtChunk = 256 << 10
	// vtStableAfter is how long a child must live for its restart delay
	// to start again from the shortest.
	vtStableAfter = time.Minute
)

// vtBackoff is the wait before each restart after a crash, in order. The
// last one repeats, so a child that always crashes on the same output tries
// again only now and then, until that output leaves the ring.
var vtBackoff = []time.Duration{
	200 * time.Millisecond, time.Second, 5 * time.Second, 30 * time.Second, 5 * time.Minute,
}

// ErrVTDown is returned while the shadow terminal is not running, for
// example between a crash and its restart.
var ErrVTDown = errors.New("the shadow terminal is not running. Try: attach again in a moment")

// VTOptions configure a VT.
type VTOptions struct {
	// Path is the shadow terminal executable.
	Path string
	// Cols and Rows are the starting size. Zero means 80 by 24.
	Cols, Rows int
	// Scrollback is how many lines the shadow terminal keeps. Zero means
	// its default, 10,000.
	Scrollback int
	// History returns the session's recent output, and the stream offset of
	// its first byte. Each new child is fed it, at start and after a crash.
	// It is called with no VT lock held. It should take the lock that
	// serialises the calls to Feed, so that each byte is fed exactly once.
	// It may be nil.
	History func() (data []byte, start int64)
	// OnWrite gets bytes to write to the PTY: replies to the agent's
	// terminal queries, sent only while AnswerQueries is on.
	OnWrite func(p []byte)
	// OnEvent gets the events the session's output raises. Events in the
	// history replayed after a crash are not raised again.
	//
	// OnWrite and OnEvent run one at a time on the VT's own goroutine. They
	// must not block for long, or the child waits for them. Both may be nil.
	OnEvent func(e VTEvent)
	// Log, if set, gets a line for each crash and restart, and each line
	// the child writes to stderr.
	Log *slog.Logger
}

// VTEvent is something the session's output asked the terminal for.
type VTEvent struct {
	// Type is "bell", "notify" (OSC 9 or 777), "title" (OSC 0 or 2),
	// "pwd" (OSC 7), "progress" (OSC 9;4) or "status" (OSC 7501).
	Type string `json:"type"`
	// Title is the notification's title, or the new window title.
	Title string `json:"title,omitempty"`
	// Body is the notification's text.
	Body string `json:"body,omitempty"`
	// State is the progress state (remove, set, error, indeterminate,
	// pause) or the program's status (idle, working, done, blocked, error,
	// clear).
	State string `json:"state,omitempty"`
	// Value is the progress in percent, or -1 if there is none.
	Value int `json:"value"`
	// Data is the pwd URL, or the status report after "7501;".
	Data string `json:"data,omitempty"`
}

// VTProfile is what the shadow terminal says about itself while it answers
// queries. Copy it from the last client's terminal. An empty field keeps
// the default: Ghostty's DA replies, "libghostty" for XTVERSION, and no
// reply to colour or colour-scheme queries.
type VTProfile struct {
	// DA1 is the primary device attributes: the conformance level, then
	// the features, for example 62, 22, 52.
	DA1 []int `json:"da1,omitempty"`
	// DA2 is the secondary device attributes: the device type, the
	// firmware version and the ROM number, for example 1, 10, 0.
	DA2 []int `json:"da2,omitempty"`
	// XTVersion is the name and version, for example "ghostty 1.3.1".
	XTVersion string `json:"xtversion,omitempty"`
	// Foreground, Background and Cursor are the default colours, as
	// "#rrggbb", for OSC 10, 11 and 12 queries.
	Foreground string `json:"fg,omitempty"`
	Background string `json:"bg,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	// ColorScheme is "dark" or "light", for CSI ? 996 n.
	ColorScheme string `json:"color_scheme,omitempty"`
}

// VT runs the shadow terminal and keeps it running. Its methods are safe
// for concurrent use. None of them wait for the child, except Snapshot,
// which waits up to its timeout, and Close, which waits up to a second for
// the child to exit.
type VT struct {
	opt       VTOptions
	log       *slog.Logger // opt.Log, or one that drops every line
	maxBehind int

	mu         sync.Mutex
	child      *vtChild
	closed     bool
	done       chan struct{}
	cols, rows int
	xpx, ypx   int
	answering  bool
	profile    []byte // JSON, nil if none was set
	fed        int64  // the stream offset after the last byte fed
	collecting bool   // true while a restart reads the history
	pending    []vtChunkAt
	crashes    int // crashes since the last stable child
}

type vtChunkAt struct {
	off  int64
	data []byte
}

// vtChild is one run of the shadow terminal process.
type vtChild struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	started time.Time
	wake    chan struct{}
	gone    chan struct{} // closed when the process has exited

	// Guarded by VT.mu.
	queue   [][]byte // frames waiting for the writer
	queued  int      // bytes in queue and in the writer's hands
	limit   int
	muted   bool // drop events while the history is replayed
	waiters map[byte][]chan vtReply
	dropped bool // killed for falling behind
	dead    bool
}

type vtReply struct {
	p   []byte
	err error
}

// StartVT starts the shadow terminal and feeds it the history, if there is
// one. It calls History before it returns, so do not hold the lock that
// History takes.
func StartVT(opt VTOptions) (*VT, error) {
	if opt.Cols <= 0 {
		opt.Cols = 80
	}
	if opt.Rows <= 0 {
		opt.Rows = 24
	}
	v := &VT{opt: opt, log: opt.Log, maxBehind: vtMaxBehind, done: make(chan struct{}), cols: opt.Cols, rows: opt.Rows}
	if v.log == nil {
		v.log = slog.New(slog.DiscardHandler)
	}
	if err := v.start(); err != nil {
		return nil, err
	}
	return v, nil
}

// Feed gives the shadow terminal the session's output. off is the stream
// offset of p[0]. It never blocks: if the child falls far behind, it is
// dropped and rebuilt from the history.
func (v *VT) Feed(off int64, p []byte) {
	if len(p) == 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return
	}
	v.fed = max(v.fed, off+int64(len(p)))
	if v.collecting {
		// A restart is reading the history. Keep what may come after it.
		v.pending = append(v.pending, vtChunkAt{off, bytes.Clone(p)})
	}
	c := v.child
	if c == nil {
		return
	}
	if c.dropped {
		return
	}
	if c.queued+len(p) > c.limit {
		// It is stuck or too slow. The history will catch up a new one.
		v.log.Warn("shadow terminal fell behind, restarting it", "queued", c.queued)
		c.dropped = true
		c.kill()
		return
	}
	for len(p) > 0 {
		n := min(len(p), vtChunk)
		c.push(vtFrame(vtFrameData, p[:n]))
		p = p[n:]
	}
}

// Resize sets the terminal size: columns and rows, and the window's size in
// pixels if it is known (zero if not).
func (v *VT) Resize(cols, rows, xpx, ypx int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.cols, v.rows, v.xpx, v.ypx = cols, rows, xpx, ypx
	if v.child != nil {
		v.child.push(v.resizeFrame())
	}
}

// Snapshot returns the bytes that redraw the session in a blank terminal of
// cols by rows: up to maxLines lines of scrollback, the screen, the cursor
// and the modes. It also returns the stream offset the snapshot reaches:
// send the client the output from that offset on, and it misses nothing
// and sees nothing twice. It resizes the shadow terminal to cols by rows,
// as the PTY is about to be. It waits at most timeout for the child.
//
// Write it with the cursor at the start of an empty line, with only empty
// lines below; anything above scrolls into the terminal's scrollback.
func (v *VT) Snapshot(cols, rows, maxLines int, timeout time.Duration) (snap []byte, upTo int64, err error) {
	if cols <= 0 || rows <= 0 {
		return nil, 0, fmt.Errorf("snapshot size %dx%d is not valid. Try: send the terminal size", cols, rows)
	}
	v.mu.Lock()
	v.cols, v.rows = cols, rows
	v.mu.Unlock()
	p := snapshotArgs(cols, rows, maxLines)
	out, upTo, err := v.request(vtFrameSnapshot, func() []byte { return p }, timeout)
	if err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		return nil, 0, errors.New("the shadow terminal could not make a snapshot. Try: attach again")
	}
	return out, upTo, nil
}

// Screen is a snapshot of the screen alone, for a preview: the bytes that
// redraw the screen as it is now, at the shadow terminal's own size, and
// the stream offset they reach. Unlike Snapshot, it changes nothing: the
// child resizes only to a new size, so the PTY keeps its size too, and a
// client can stay attached.
func (v *VT) Screen(timeout time.Duration) (snap []byte, upTo int64, err error) {
	// The size is read as the frame is sent, under the lock that orders
	// the frames, so a resize cannot come between and be undone.
	out, upTo, err := v.request(vtFrameSnapshot, func() []byte { return snapshotArgs(v.cols, v.rows, 0) }, timeout)
	if err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		return nil, 0, errors.New("the shadow terminal could not make a snapshot")
	}
	return out, upTo, nil
}

// snapshotArgs is the payload of a snapshot request.
func snapshotArgs(cols, rows, maxLines int) []byte {
	var p [8]byte
	binary.LittleEndian.PutUint16(p[0:], clamp16(cols))
	binary.LittleEndian.PutUint16(p[2:], clamp16(rows))
	binary.LittleEndian.PutUint32(p[4:], clamp32(maxLines))
	return p[:]
}

// AnswerQueries turns on or off the shadow terminal's replies to the
// agent's terminal queries (OnWrite). Turn it on while no client is
// attached, and off while one is, so the client's terminal answers.
func (v *VT) AnswerQueries(on bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.answering = on
	if v.child != nil {
		v.child.push(v.queriesFrame())
	}
}

// SetProfile sets what the shadow terminal reports while it answers queries.
func (v *VT) SetProfile(p VTProfile) {
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.profile = b
	if v.child != nil {
		v.child.push(vtFrame(vtFrameProfile, b))
	}
}

// Close stops the shadow terminal for good.
func (v *VT) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	close(v.done)
	c := v.child
	v.child = nil
	if c != nil {
		c.fail(ErrVTDown)
	}
	v.mu.Unlock()
	if c != nil {
		// End of input makes the child exit. Kill it if it does not.
		_ = c.stdin.Close()
		select {
		case <-c.gone:
		case <-time.After(time.Second):
			c.kill()
			<-c.gone
		}
	}
	return nil
}

// dump returns a JSON description of the screen, for tests.
func (v *VT) dump(maxLines int, timeout time.Duration) ([]byte, error) {
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], clamp32(maxLines))
	out, _, err := v.request(vtFrameDump, func() []byte { return p[:] }, timeout)
	return out, err
}

// sync returns once the child has handled everything sent before it, or
// after 5 seconds with an error.
func (v *VT) sync() error {
	_, _, err := v.request(vtFrameSync, func() []byte { return nil }, 5*time.Second)
	return err
}

// request sends a frame and waits for the reply of the same type. Replies
// come in order, so each one goes to the oldest waiter. payload makes the
// frame's payload, with v.mu held.
// It also returns the stream offset the child has been fed up to when it
// gets the request.
func (v *VT) request(kind byte, payload func() []byte, timeout time.Duration) ([]byte, int64, error) {
	ch := make(chan vtReply, 1)
	v.mu.Lock()
	c := v.child
	if c == nil || v.closed {
		v.mu.Unlock()
		return nil, 0, ErrVTDown
	}
	fed := v.fed
	c.waiters[kind] = append(c.waiters[kind], ch)
	c.push(vtFrame(kind, payload()))
	v.mu.Unlock()

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.p, fed, r.err
	case <-t.C:
		return nil, 0, fmt.Errorf("the shadow terminal did not answer within %v. Try: attach again", timeout)
	}
}
