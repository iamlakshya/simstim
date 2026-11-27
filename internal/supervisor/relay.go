package supervisor

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Size caps on frames that pass from the holder to the client (spec 6.3,
// 20.10). A holder's hello comes first, and the supervisor reads it itself
// (skipHello). Any other type, or any payload over its type's cap, closes
// the connection before its payload is allocated.
const (
	helloMax = 4 << 10
	smallMax = 4 << 10
	errMax   = 4 << 10
	endMax   = 4 << 10
	respMax  = 2 << 20
)

// maxPayload returns the largest payload that the supervisor takes for t
// from the holder, or -1 when a holder never sends t to a client.
func maxPayload(t proto.Type) int {
	switch t {
	case proto.TError:
		return errMax
	case proto.TEnded:
		return endMax
	case proto.TPong, proto.TAttached:
		return smallMax
	case proto.TResponse:
		return respMax
	case proto.TOutput, proto.TSnapshot:
		return proto.MaxFrame - 1
	default:
		return -1
	}
}

// errFrame is the error of a frame from the holder that may not pass.
var errFrame = errors.New("a frame that may not pass")

// head reads a frame's length and type. n is the payload's size, after the
// type byte. It refuses an empty frame and one over proto.MaxFrame, before
// it reads the payload.
func head(r *bufio.Reader) (t proto.Type, n int, err error) {
	var h [5]byte
	if _, err := io.ReadFull(r, h[:4]); err != nil {
		return 0, 0, fmt.Errorf("read a frame: %w", err)
	}
	size := binary.LittleEndian.Uint32(h[:4])
	if size == 0 || size > proto.MaxFrame {
		return 0, 0, fmt.Errorf("%w: a frame of %d bytes", errFrame, size)
	}
	if _, err := io.ReadFull(r, h[4:]); err != nil {
		return 0, 0, fmt.Errorf("read a frame: %w", err)
	}
	return proto.Type(h[4]), int(size) - 1, nil
}

// skipHello reads the holder's hello and drops it: the supervisor answered
// the client's hello itself.
func skipHello(r *bufio.Reader) error {
	t, n, err := head(r)
	if err != nil {
		return err
	}
	if t != proto.THello || n > helloMax {
		return fmt.Errorf("%w: the holder's hello has type %#02x and %d bytes", errFrame, byte(t), n)
	}
	if _, err := r.Discard(n); err != nil {
		return fmt.Errorf("read the holder's hello: %w", err)
	}
	return nil
}

// frameStall bounds the wait for the rest of a frame from the holder, once
// its head has come.
var frameStall = 10 * time.Second

// keepBuf is the largest buffer that relay keeps per stream for the next
// frame. Larger frames draw from maxRelayBytes across the whole supervisor
// and drop their buffer before the next Peek.
const (
	keepBuf       = 16 << 10
	maxRelayBytes = 8 << 20
)

// takeRelay reserves n bytes of the supervisor's large-frame budget, waiting
// until there is room, the frame's deadline passes or its connection ends.
func (s *Supervisor) takeRelay(c *client, n int, deadline time.Time) error {
	want := int64(n)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var linkDone <-chan struct{}
	if s.link != nil {
		linkDone = s.link.Done()
	}
	for {
		if !time.Now().Before(deadline) {
			return os.ErrDeadlineExceeded
		}
		s.relayMu.Lock()
		if s.relayUsed == 0 || s.relayUsed+want <= maxRelayBytes {
			s.relayUsed += want
			s.relayMu.Unlock()
			return nil
		}
		if s.relayWake == nil {
			s.relayWake = make(chan struct{})
		}
		wake := s.relayWake
		s.relayMu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return os.ErrDeadlineExceeded
		case <-c.done:
			return net.ErrClosed
		case <-linkDone:
			return net.ErrClosed
		}
	}
}

// giveRelay returns n bytes to the supervisor's large-frame budget.
func (s *Supervisor) giveRelay(n int) {
	s.relayMu.Lock()
	s.relayUsed -= int64(n)
	wake := s.relayWake
	s.relayWake = nil
	s.relayMu.Unlock()
	if wake != nil {
		close(wake)
	}
}

// relay passes the holder's frames on the stream st to the client, in
// order, until the stream or the connection ends. It checks each frame's
// type and size before allocating its buffer, and passes output payloads
// without parsing them, so output stays byte for byte (spec 3.3, G8). It
// parses only an end, an error and a response, so guest claims never carry
// trusted error codes (passEnd, passError, passResponse). It reads each
// frame whole, within frameStall, before it writes it: a guest that stalls
// in a frame holds up no write to the client.
func (s *Supervisor) relay(c *client, st *link.Stream, r *bufio.Reader) {
	gone := false // the client went: its frames may still pass (closeSoon)
	defer func() {
		if gone {
			c.closeSoon()
		} else {
			c.close()
		}
	}()
	var small []byte
	stall := frameStall
	for {
		if _, err := r.Peek(1); err != nil {
			s.streamEnded(c, err)
			return
		}
		deadline := time.Now().Add(stall)
		_ = st.SetReadDeadline(deadline)
		t, n, err := head(r)
		if err != nil {
			_ = st.SetReadDeadline(time.Time{})
			switch {
			case errors.Is(err, errFrame), errors.Is(err, os.ErrDeadlineExceeded):
				s.refuseFrame(c, err)
			default:
				s.streamEnded(c, err)
			}
			return
		}
		limit := maxPayload(t)
		if limit < 0 {
			_ = st.SetReadDeadline(time.Time{})
			s.refuseFrame(c, fmt.Errorf("%w: type %#02x", errFrame, byte(t)))
			return
		}
		if n > limit {
			_ = st.SetReadDeadline(time.Time{})
			s.refuseFrame(c, fmt.Errorf("%w: type %#02x of %d bytes", errFrame, byte(t), n))
			return
		}
		var stop bool
		small, gone, stop = s.relayBody(c, st, r, t, n, small, deadline, stall)
		if stop {
			return
		}
	}
}

// relayBody reads one frame's payload of n bytes and writes it to c. Keeping
// any buffer larger than keepBuf local to this call drops its stack reference
// and returns its budget before relay parks in Peek again.
func (s *Supervisor) relayBody(c *client, st *link.Stream, r *bufio.Reader, t proto.Type, n int, small []byte, deadline time.Time, stall time.Duration) (keep []byte, gone, stop bool) {
	var buf []byte
	if n <= keepBuf {
		if cap(small) < keepBuf {
			small = make([]byte, keepBuf)
		}
		buf = small[:n]
	} else {
		if err := s.takeRelay(c, n, deadline); err != nil {
			_ = st.SetReadDeadline(time.Time{})
			if errors.Is(err, os.ErrDeadlineExceeded) {
				s.refuseFrame(c, fmt.Errorf("%w: a frame of %d bytes exceeded %s while waiting for relay space", errFrame, n, stall))
			} else {
				s.streamEnded(c, err)
			}
			return small, false, true
		}
		defer s.giveRelay(n)
		buf = make([]byte, n)
	}
	_, err := io.ReadFull(r, buf)
	_ = st.SetReadDeadline(time.Time{})
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		s.refuseFrame(c, fmt.Errorf("%w: a frame of %d bytes that did not come whole within %s", errFrame, n, stall))
		return small, false, true
	case err != nil:
		s.streamEnded(c, err)
		return small, false, true
	}
	var out []byte
	switch t {
	case proto.TEnded:
		out, err = s.rewriteEnd(buf)
	case proto.TError:
		out, err = rewriteError(buf)
	case proto.TResponse:
		out, err = rewriteResponse(buf)
	default:
		if t == proto.TOutput {
			s.traceUpperOutputV1(c)
			s.mu.Lock()
			s.active(time.Now())
			s.mu.Unlock()
		}
		out = buf
	}
	if err != nil {
		s.refuseFrame(c, err)
		return small, false, true
	}
	if c.write(t, out) != nil {
		return small, true, true
	}
	return small, false, false
}

// streamEnded tells the client that the guest closed its holder stream,
// when the run goes on and the client did not close the connection
// itself. When the run ends, the client hears nothing here: its bridge
// says that the session stopped, which is then the trusted side's fact.
func (s *Supervisor) streamEnded(c *client, err error) {
	select {
	case <-c.done:
		return
	default:
	}
	if s.runEnds() {
		return
	}
	c.errOnce.Do(func() {
		s.log.Warn("closed a connection: the guest closed its holder stream", "err", err)
		b, _ := json.Marshal(errGuestClosed("the session's sandbox closed the connection"))
		_ = c.write(proto.TError, b)
	})
}

// refuseFrame tells the client that the guest sent a frame that may not
// pass, before the connection closes.
func (s *Supervisor) refuseFrame(c *client, err error) {
	s.log.Warn("closed a connection: a frame from the guest that may not pass", "err", err)
	b, _ := json.Marshal(proto.Error{Code: "guest_frame", Message: "the session's sandbox sent a frame that may not pass, so the connection closed",
		Hint: "Attach again. If it happens again, read the session's supervisor.log on the host."})
	_ = c.write(proto.TError, b)
}

// rewriteError rewrites an error frame from the holder, payload b, as the
// guest's: its code becomes guest_error, whatever the guest said, its
// message is cleaned and says that the sandbox said it, and its hint is
// the trusted side's. So a guest never sends a code that a client acts
// on, such as the one that says the session stopped.
func rewriteError(b []byte) ([]byte, error) {
	if len(b) > errMax {
		return nil, fmt.Errorf("%w: an error of %d bytes", errFrame, len(b))
	}
	var pe proto.Error
	if json.Unmarshal(b, &pe) != nil {
		return nil, fmt.Errorf("%w: an error that is not valid", errFrame)
	}
	out, _ := json.Marshal(proto.Error{Code: "guest_error", Message: "the sandbox says: " + cleanClaim(pe.Message, maxReason),
		Hint: "Attach again, or resume the session."})
	return out, nil
}

// rewriteResponse rewrites an answer to a forwarded request (peek, ports,
// send) from the holder: an error in the answer gets the same guest_error
// attribution and trusted hint as an error frame, with no guest Data or
// Result, and a good answer carries no Error.
func rewriteResponse(b []byte) ([]byte, error) {
	if len(b) > respMax {
		return nil, fmt.Errorf("%w: a response of %d bytes", errFrame, len(b))
	}
	var resp proto.Response
	if json.Unmarshal(b, &resp) != nil {
		return nil, fmt.Errorf("%w: a response that is not valid", errFrame)
	}
	if !resp.OK {
		rawMsg := ""
		if resp.Error != nil {
			rawMsg = resp.Error.Message
		}
		out, _ := json.Marshal(proto.Response{
			ID: resp.ID,
			OK: false,
			Error: &proto.Error{
				Code:    "guest_error",
				Message: "the sandbox says: " + cleanClaim(rawMsg, maxReason),
				Hint:    "Check the session in the sandbox, or resume it.",
			},
		})
		return out, nil
	}
	out, _ := json.Marshal(proto.Response{ID: resp.ID, OK: true, Result: resp.Result})
	return out, nil
}

// rewriteEnd rewrites the agent's end, an end frame from the holder with
// payload b, with its exit code and signal marked as claimed. Whether the
// memory cap stopped the agent is the trusted side's own fact (endOOM),
// never the guest's.
func (s *Supervisor) rewriteEnd(b []byte) ([]byte, error) {
	if len(b) > endMax {
		return nil, fmt.Errorf("%w: an end of %d bytes", errFrame, len(b))
	}
	var e proto.Ended
	if json.Unmarshal(b, &e) != nil || e.ExitCode < 0 || e.ExitCode > 255 {
		// -1 is the trusted side's own stop, and no exit code is outside
		// 0 to 255.
		return nil, fmt.Errorf("%w: an end that is not valid", errFrame)
	}
	e.Signal = cleanClaim(e.Signal, maxSignal)
	e.OOM, e.Claimed = s.endOOM(e.ExitCode, e.Signal), true
	out, _ := json.Marshal(e)
	return out, nil
}
