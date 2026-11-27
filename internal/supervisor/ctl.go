package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/pullexport"
)

// The ctl stream carries the supervisor's ops to the guest init, and the
// guest's events back (spec 10.3). The supervisor reads it with a cap of
// maxEvent on each frame, and at most eventRate events a second. A frame
// over the cap, or of a type that the stream does not carry, closes the
// link: the guest broke the protocol. So does the stream's end while the
// link lasts. A request that the guest stalls past its deadline, by
// granting no credit or by giving no answer, closes the link too.

// maxEvent is the largest frame that the supervisor reads from the ctl
// stream (spec 5.3).
const maxEvent = 64 << 10

// eventRate is how many events a second the supervisor takes. It drops
// the rest (spec 10.3), but never the agent's first end.
const eventRate = 20

// dropReport is how often the audit log may say that events were dropped.
const dropReport = time.Minute

// ctlConn is the supervisor's end of the ctl stream.
type ctlConn struct {
	sendTurn chan struct{}
	sendOnce sync.Once
	s        *Supervisor
	st       *link.Stream
	w        *proto.Writer

	mu         sync.Mutex
	next       uint64
	waits      map[uint64]chan proto.Response
	progress   map[uint64]func()
	rate       rateLimit
	stateReady bool
	memberEnds map[string]bool
	ended      bool          // an end passed the rate
	dropped    int64         // events over the rate since the last report
	reported   time.Time     // when the audit log last said so
	done       chan struct{} // closed when the reader has ended
}

// newCtl starts reading the ctl stream st.
func newCtl(s *Supervisor, st *link.Stream) *ctlConn {
	c := &ctlConn{sendTurn: make(chan struct{}, 1), s: s, st: st, w: proto.NewWriter(st), waits: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		c.read()
	}()
	return c
}

// request sends op with args, waits for the answer and decodes its result
// into out, when out is not nil. The write honours ctx's deadline, and a
// request that ctx ends closes the link.
func (c *ctlConn) request(ctx context.Context, op string, args, out any) error {
	return c.requestMode(ctx, op, args, out, false)
}

func (c *ctlConn) requestDetached(ctx context.Context, op string, args, out any) error {
	return c.requestMode(ctx, op, args, out, true)
}

func (c *ctlConn) requestMode(ctx context.Context, op string, args, out any, isolatedCancel bool) error {
	b, err := json.Marshal(args)
	if err != nil {
		return &ctlUnsentError{cause: fmt.Errorf("encode %s: %w", op, err)}
	}
	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	c.next++
	id := c.next
	c.waits[id] = ch
	if op == proto.CtlBundlePrepare {
		if c.progress == nil {
			c.progress = make(map[uint64]func())
		}
		c.progress[id] = pullexport.CaptureProgress(ctx)
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waits, id)
		delete(c.progress, id)
		c.mu.Unlock()
	}()
	sendCtx := ctx
	finishSend := func() {}
	if isolatedCancel {
		if err := ctx.Err(); err != nil {
			return &ctlUnsentError{cause: err}
		}
		sendCtx, finishSend = phaseclock.WithDeadline(c.s.lifetimeContext(), now().Add(clientWriteWait))
	}
	err = c.sendRequest(sendCtx, proto.Request{ID: id, Op: op, Args: b})
	finishSend()
	if err != nil {
		return err
	}
	select {
	case resp := <-ch:
		return answerOf(op, resp, out)
	case <-ctx.Done():
		if isolatedCancel {
			return ctx.Err()
		}
		c.s.loseLink("the guest init did not answer " + op)
		return wallError("the guest init did not answer "+op, ctx.Err())
	case <-c.done:
		// The reader has passed on every answer that came before the
		// stream's end. One may wait in ch still.
		select {
		case resp := <-ch:
			return answerOf(op, resp, out)
		default:
		}
		return wallError(c.s.lostReason(), c.s.link.Err())
	}
}

// answerOf is the outcome of op from its answer, whose result it decodes
// into out, when out is not nil.
func answerOf(op string, resp proto.Response, out any) error {
	if resp.Error != nil {
		return claimedError(resp.Error)
	}
	if !resp.OK {
		return wallError("the guest init returned an unsuccessful answer to "+op, errors.New("unsuccessful control response without an error"))
	}
	if out != nil && json.Unmarshal(resp.Result, out) != nil {
		return wallError("the guest init's answer to "+op+" is not valid", errors.New("bad JSON"))
	}
	return nil
}

// read reads the ctl stream until it ends. A frame over the cap, or of a
// type other than a response or an event, closes the link. So does the
// stream's end, while the link lasts.
func (c *ctlConn) read() {
	r := bufio.NewReaderSize(c.st, maxEvent)
	for {
		f, err := readCapped(r)
		if err != nil {
			switch {
			case errors.Is(err, errFrame):
				c.s.breach(err)
			case errors.Is(err, io.EOF) && (c.s.captureWillClose() || c.s.pendingEmpty()):
				// Ready is only a claim. wait still requires the actual clean owned exit.
			case errors.Is(err, io.EOF):
				c.s.loseLink("the guest init closed its ctl stream")
			default:
				c.s.loseLink("the sandbox's link closed")
			}
			return
		}
		switch f.Type {
		case proto.TResponse:
			c.answer(f.Payload)
		case proto.TEvent:
			c.capturePulse(f.Payload)
			if c.allow(time.Now(), f.Payload) {
				c.s.event(f.Payload)
			}
		default:
			c.s.breach(fmt.Errorf("%w: type %#02x on the ctl stream", errFrame, byte(f.Type)))
			return
		}
	}
}

func (c *ctlConn) capturePulse(raw []byte) {
	var event proto.CtlEvent
	if json.Unmarshal(raw, &event) != nil || event.Event != proto.EventHeartbeat {
		return
	}
	c.mu.Lock()
	callbacks := make([]func(), 0, len(c.progress))
	for _, callback := range c.progress {
		callbacks = append(callbacks, callback)
	}
	c.mu.Unlock()
	for _, callback := range callbacks {
		callback() // same pending capture and live outer context, never after end
	}
}

// answer passes a response to the request that waits for it.
func (c *ctlConn) answer(p []byte) {
	var resp proto.Response
	if json.Unmarshal(p, &resp) != nil {
		return
	}
	c.mu.Lock()
	ch := c.waits[resp.ID]
	c.mu.Unlock()
	if ch != nil {
		select {
		case ch <- resp:
		default:
		}
	}
}

// allow reports whether the event p at t fits the rate, and counts it if
// not. The first valid end of the agent always passes: it is the one event
// that nothing later says again. An end that parseEvent would drop never
// takes that room.
func (c *ctlConn) allow(t time.Time, p []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.stateReady && c.s.validCaptureMessage(p) {
		c.stateReady = true
		return true
	}
	endID := c.s.validMemberEnd(p)
	mark := func() {
		if endID == "" {
			return
		}
		if c.memberEnds == nil {
			c.memberEnds = make(map[string]bool)
		}
		c.memberEnds[endID] = true
		if endID == c.s.id {
			c.ended = true
		}
	}
	if c.rate.allow(t) {
		mark()
		return true
	}
	if endID != "" && !c.memberEnds[endID] {
		mark()
		return true
	}
	c.dropped++
	return false
}

// drops returns how many events the rate dropped since the last report,
// at most once each dropReport, or 0.
func (c *ctlConn) drops(t time.Time) int64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dropped == 0 || t.Sub(c.reported) < dropReport {
		return 0
	}
	n := c.dropped
	c.dropped, c.reported = 0, t
	return n
}

// readCapped reads one frame of at most maxEvent bytes, its type byte
// included. It refuses a larger frame before it reads its body.
func readCapped(r *bufio.Reader) (proto.Frame, error) {
	t, n, err := head(r)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return proto.Frame{}, fmt.Errorf("%w: a frame cut short", errFrame)
		}
		return proto.Frame{}, err
	}
	if n+1 > maxEvent {
		return proto.Frame{}, fmt.Errorf("%w: a frame of %d bytes on the ctl stream, over %d", errFrame, n+1, maxEvent)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return proto.Frame{}, fmt.Errorf("%w: a frame cut short", errFrame)
	}
	return proto.Frame{Type: t, Payload: buf}, nil
}

// rateLimit is a bucket of eventRate tokens, which fills again at
// eventRate a second.
type rateLimit struct {
	tokens float64
	last   time.Time
}

// allow takes a token at t, when one is left.
func (l *rateLimit) allow(t time.Time) bool {
	if l.last.IsZero() {
		l.tokens = eventRate
	} else {
		l.tokens = min(eventRate, l.tokens+t.Sub(l.last).Seconds()*eventRate)
	}
	l.last = t
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// breach closes the link when the guest broke the protocol of the ctl
// stream.
func (s *Supervisor) breach(err error) {
	s.log.Warn("closing the link: the guest broke the protocol", "err", err)
	s.audit.write("link_breach", map[string]any{"reason": "ctl"})
	s.loseLink("the sandbox broke the link's rules")
}

func (c *ctlConn) sendRequest(ctx context.Context, req proto.Request) error {
	encoded, err := json.Marshal(req)
	if err != nil {
		return &ctlUnsentError{cause: err}
	}
	if len(encoded)+1 > maxEvent {
		return &ctlUnsentError{cause: &proto.Error{Code: "pull_metadata", Message: "the actual control request exceeds its frame bound", Hint: "Use metadata representable in the existing control protocol."}}
	}
	c.sendOnce.Do(func() {
		if c.sendTurn == nil {
			c.sendTurn = make(chan struct{}, 1)
		}
	})
	select {
	case c.sendTurn <- struct{}{}:
	case <-ctx.Done():
		return &ctlUnsentError{cause: ctx.Err()}
	}
	defer func() { <-c.sendTurn }()
	if err := ctx.Err(); err != nil {
		return &ctlUnsentError{cause: err}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.st.SetWriteDeadline(dl)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = c.st.SetWriteDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = c.st.SetWriteDeadline(time.Time{})
	}()
	if err := c.w.WriteJSON(proto.TRequest, req); err != nil {
		c.s.loseLink("the guest init took no " + req.Op)
		return wallError("the guest init took no "+req.Op, err)
	}
	return nil
}
