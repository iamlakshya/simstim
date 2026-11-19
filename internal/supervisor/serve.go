package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The supervisor serves only paths.Socket(id). It answers some requests, passes
// others and the attach frames to the holder inside the wall over a holder
// stream, and refuses the rest (spec 6.3 and 10.4).

// Features are what the supervisor offers in its hello (spec 10.5).
var Features = []string{api.FeatureSandboxCohort, inputtransfer.Feature, inputtransfer.PrimaryPolicyFeature, inputtransfer.FinishFeature, "wall", host.SavedRenameFeature, api.FeatureExecStream, api.FeatureSpawn, api.FeatureSandboxSnapshot, api.FeaturePullBundle, api.FeaturePullOutputProgress, api.FeatureBoundSecrets}

// listenHint is the hint of an error of the session's socket.
const listenHint = "Check the host's runtime folder ($XDG_RUNTIME_DIR) and its free space, then resume the session."

// listen opens the session's socket, 0600 in a 0700 folder.
func (s *Supervisor) listen() error {
	sock := paths.Socket(s.id)
	for _, dir := range []string{filepath.Dir(filepath.Dir(sock)), filepath.Dir(sock)} {
		if err := paths.EnsurePrivateDir(dir); err != nil {
			return &proto.Error{Code: "listen", Message: "the supervisor cannot make the sockets' folder: " + err.Error(), Hint: listenHint}
		}
	}
	_ = os.Remove(sock)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		return &proto.Error{Code: "listen", Message: "the supervisor cannot listen on the session's socket: " + err.Error(), Hint: listenHint}
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return &proto.Error{Code: "listen", Message: "the supervisor cannot make the session's socket private: " + err.Error(), Hint: listenHint}
	}
	s.ln = ln
	return nil
}

// errIncompatible answers a client whose hello the supervisor cannot take.
var errIncompatible = proto.Error{Code: "protocol", Message: "incompatible client",
	Hint: "Use a client and a host helper of the same version, then connect again."}

// closeSocket closes the session's socket and removes its file.
func (s *Supervisor) closeSocket() {
	if s.ln != nil {
		s.ln.Close()
	}
	os.Remove(paths.Socket(s.id))
}

// acceptLoop serves each connection to the session's socket.
func (s *Supervisor) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(conn)
	}
}

// client is one connection to the session's socket.
type client struct {
	imageSaveMu     sync.Mutex
	imageSave       *imageSaveJobV1
	imageSaveClosed bool
	copyMu          sync.Mutex
	copyBusy        bool
	copyPlan        *copyReadPlan
	pullMu          sync.Mutex
	pull            *host.PullRPC
	pullClosed      bool
	conn            net.Conn
	full            bool   // the peer may do everything (host.FullPeer)
	hello           []byte // the client's hello, which the holder gets too
	wmu             sync.Mutex
	w               *proto.Writer
	// writeWait bounds one output frame. writeErr prevents a second frame
	// from following a partially written first frame.
	writeWait time.Duration
	writeErr  error

	gmu     sync.Mutex
	gs      *link.Stream // the holder stream, once a frame needs it
	watch   io.Closer    // the watch of the client's end, with gs
	once    sync.Once
	soon    sync.Once
	errOnce sync.Once
	// in holds the frames for the holder, which forward writes to gs, in
	// order. queued counts their bytes. Past maxQueued, the client's reader
	// waits for room (pass): no frame is refused for its size, and every
	// byte reaches the holder as the client sent it. When the client goes,
	// the frames that it sent first still pass, for up to drainWait
	// (closeSoon): only a guest that takes none of them loses them.
	in     chan proto.Frame
	queued atomic.Int64
	room   chan struct{} // a token each time forward has taken a frame
	done   chan struct{} // closed by close
	drain  time.Duration // drainWait, when the connection came
}

// The bound of a client's frames that wait for the holder. A frame larger
// than maxQueued waits until the queue is empty, then passes alone.
const (
	maxQueued       = 1 << 20
	maxQueuedFrames = 1024
)

// drainWait bounds how long the frames of a client that went may take to
// reach the holder, before its connection closes.
var drainWait = 2 * time.Second

// reply answers one request.
func (c *client) reply(id uint64, v any, perr *proto.Error) {
	resp := proto.Response{ID: id, OK: perr == nil, Error: perr}
	if v != nil {
		resp.Result, _ = json.Marshal(v)
	}
	b, _ := json.Marshal(resp)
	_ = c.write(proto.TResponse, b)
}

// closeSoon closes the connection after drainWait: the client has gone,
// and the frames that it sent before may still pass.
func (c *client) closeSoon() {
	c.soon.Do(func() { time.AfterFunc(c.drain, c.close) })
}

// endInput ends the client's input after its clean end: forward passes the
// frames that wait, then closes the connection, within drainWait. The
// reader calls it once, after its last frame.
func (c *client) endInput() {
	if !c.hasGuest() {
		c.close()
		return
	}
	close(c.in)
	c.closeSoon()
}

// close ends the connection and its holder stream.
func (c *client) close() {
	c.once.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		c.conn.Close()
		c.closeImageSaveV1()
		c.closePull()
		c.gmu.Lock()
		gs, watch := c.gs, c.watch
		c.gmu.Unlock()
		if watch != nil {
			_ = watch.Close()
		}
		if gs != nil {
			_ = gs.Close()
		}
	})
}

// serve answers the hello of one connection, then its frames, until it
// ends or a refused frame closes it.
func (s *Supervisor) serve(conn net.Conn) {
	c := &client{conn: conn, w: proto.NewWriter(conn), in: make(chan proto.Frame, maxQueuedFrames),
		room: make(chan struct{}, 1), done: make(chan struct{}), drain: drainWait, writeWait: clientWriteWait}
	ended := false // the client ended its connection itself
	defer func() {
		s.drop(c)
		if ended {
			c.endInput()
		} else {
			c.close()
		}
	}()
	r := bufio.NewReaderSize(conn, 64<<10)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return
	}
	var hello proto.Hello
	if json.Unmarshal(f.Payload, &hello) != nil || hello.Check(version.Protocol) != nil {
		b, _ := json.Marshal(errIncompatible)
		_ = c.write(proto.TError, b)
		return
	}
	c.hello = append([]byte(nil), f.Payload...)
	c.full = host.FullPeer(conn, s.id, s.peerCheck)
	b, _ := json.Marshal(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "supervisor", Features: s.sessionFeatures()})
	if c.write(proto.THello, b) != nil {
		return
	}
	firstFrame := true
	for {
		f, err := proto.Read(r)
		if err != nil {
			ended = errors.Is(err, io.EOF)
			return
		}
		wasFirst := firstFrame
		firstFrame = false
		if !c.full {
			if !s.serveLimited(c, f) {
				return
			}
			continue
		}
		if f.Type == proto.TRequest {
			var request proto.Request
			if json.Unmarshal(f.Payload, &request) == nil && request.Op == "events.watch" {
				if !wasFirst {
					c.reply(request.ID, nil, toProtoErr(execRefusal("events need a dedicated connection")))
					return
				}
				_ = s.serveEvents(c, r, request)
				return
			}
		}
		if s.reverseFrame(c, r, f, wasFirst) {
			return
		}
		if s.uploadFrame(c, r, f, wasFirst) {
			return
		}
		if s.inputFrame(c, r, f, wasFirst) {
			return
		}
		if s.execFrame(c, r, f, wasFirst) {
			return
		}

		if !s.frame(c, f) {
			return
		}
	}
}

// serveLimited answers a frame from a program inside a session: a pong for
// a ping, and forbidden for anything else, which then closes the
// connection (holder_peer.go).
func (s *Supervisor) serveLimited(c *client, f proto.Frame) bool {
	switch f.Type {
	case proto.TPing:
		return c.write(proto.TPong, f.Payload) == nil
	case proto.TRequest:
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) == nil {
			c.reply(req.ID, nil, host.ErrInside)
		}
	default:
		b, _ := json.Marshal(host.ErrInside)
		_ = c.write(proto.TError, b)
	}
	s.log.Warn("refused a frame from inside a session", "type", int(f.Type))
	return false
}

// frame handles one frame from a client with full access. It reports
// false when the connection must close: a frame that may not pass, or a
// holder stream that failed.
func (s *Supervisor) frame(c *client, f proto.Frame) bool {
	switch f.Type {
	case proto.TEvent:
		return s.pullProgress(c, f.Payload)
	case proto.TRequest:
		return s.request(c, f)
	case proto.TAttach:
		var a proto.Attach
		if json.Unmarshal(f.Payload, &a) != nil {
			return false
		}
		s.noteAttach(c, a)
	case proto.TInput:
		s.noteInput(c, f.Payload)
	case proto.TResize:
		var rz proto.Resize
		if json.Unmarshal(f.Payload, &rz) != nil {
			return false
		}
		s.noteResize(c, rz)
	case proto.TDetach:
		s.drop(c)
	case proto.TTrace:
	case proto.TPing:
		if !c.hasGuest() {
			return c.write(proto.TPong, f.Payload) == nil
		}
	default:
		// No other frame passes to the guest (spec 6.3).
		s.log.Warn("refused a frame to the guest", "type", int(f.Type))
		return false
	}
	return s.pass(c, f)
}

// hasGuest reports whether the connection has its holder stream.
func (c *client) hasGuest() bool {
	c.gmu.Lock()
	defer c.gmu.Unlock()
	return c.gs != nil
}

// pass queues a frame for the holder, on the connection's holder stream.
// When the queue is full, it waits for room: a guest that takes no input
// holds up this client's reads, never its bytes. The client's end, or the
// connection's close, ends the wait.
func (s *Supervisor) pass(c *client, f proto.Frame) bool {
	if err := s.guest(c); err != nil {
		s.log.Warn("no holder stream", "err", err)
		s.streamFailed(c, "the session's sandbox did not take the connection")
		return false
	}
	n := int64(len(f.Payload))
	for q := c.queued.Load(); q > 0 && q+n > maxQueued; q = c.queued.Load() {
		select {
		case <-c.room:
		case <-c.done:
			return false
		}
	}
	c.queued.Add(n)
	select {
	case c.in <- f:
		return true
	case <-c.done:
		return false
	}
}

// forward writes the client's frames to the holder stream w, in order,
// until the connection closes. A write that fails tells the client that
// the guest closed the stream, when the run goes on, then closes it.
func (s *Supervisor) forward(c *client, w *proto.Writer) {
	for {
		select {
		case f, ok := <-c.in:
			if !ok {
				// The client ended, and each of its frames has passed.
				c.close()
				return
			}
			err := w.Write(f.Type, f.Payload)
			c.queued.Add(-int64(len(f.Payload)))
			select {
			case c.room <- struct{}{}:
			default:
			}
			if err != nil {
				s.streamEnded(c, err)
				c.close()
				return
			}
		case <-c.done:
			return
		}
	}
}

// errGuestClosed is what a client hears when the guest closed its holder
// stream, or did not take it, while the run goes on: the guest's doing,
// which never reads as the session's stop.
func errGuestClosed(msg string) proto.Error {
	return proto.Error{Code: "guest_closed", Message: msg + ", and the session still runs",
		Hint: "Attach again. If it happens again, read the session's supervisor.log on the host."}
}

// streamFailed tells the client that its holder stream failed. Only when
// the run ends, which the trusted side sees itself, does the client hear
// that the session stopped.
func (s *Supervisor) streamFailed(c *client, msg string) {
	pe := errGuestClosed(msg)
	if s.runEnds() {
		pe = proto.Error{Code: "session_gone", Message: "the session stopped", Hint: "Resume the session."}
	}
	b, _ := json.Marshal(pe)
	_ = c.write(proto.TError, b)
}

// runEnds reports whether the session's run ends: the supervisor stops
// it, or its link has closed, which ends it (wait).
func (s *Supervisor) runEnds() bool {
	select {
	case <-s.link.Done():
		return true
	default:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.phase == host.LifeEnding || s.rec.phase == host.LifeEnded
}
