package link

import (
	"context"
	"errors"
	"fmt"
	"math"
)

// OpenGuard is called by the sole physical writer without the link mutex.
// Its release must retain the original source until that write has joined.
// It may refuse this open without ending the link or consuming a stream ID.
// Release must be bounded and must not synchronously Close this same Session:
// it runs on the sole writer, which Close itself has to join.
type OpenGuard func(context.Context) (release func(), err error)

type guardedOpenResult struct {
	stream *Stream
	err    error
}

type guardedOpen struct {
	ctx          context.Context
	cancel       context.CancelFunc
	kind         Kind
	header       []byte
	guard        OpenGuard
	done         chan guardedOpenResult
	taken        bool        // under Session.mu
	stopWrite    func() bool // sole writer only, after ID admission
	writeStopped chan struct{}
}

// OpenGuarded admits one open at its actual writer turn. A queued cancellation
// consumes no ID. Once admitted, cancellation interrupts the raw carrier and
// the caller waits for the physical write and cancellation callback to join.
// Close cancels native admission and joins the same sole writer.
func (s *Session) OpenGuarded(ctx context.Context, kind Kind, header any, guard OpenGuard) (*Stream, error) {
	if ctx == nil || guard == nil {
		return nil, errors.New("link: a guarded open needs its original context and source guard. Try: reconnect through the original invocation")
	}
	if who, ok := s.opt.Opener[kind]; !ok || who != s.side {
		return nil, fmt.Errorf("link: the %s may not open a %q stream. Try: use the permitted side", s.side, kind)
	}
	hdr, err := encodeHeader(kind, header)
	if err != nil {
		return nil, err
	}
	if len(hdr) > s.opt.MaxHeader {
		return nil, errors.New("link: the guarded open header is too large. Try: send bounded operation data")
	}
	select {
	case <-s.hello:
	case <-s.done:
		return nil, s.Err()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	owned, cancel := context.WithCancel(ctx)
	defer cancel()
	request := &guardedOpen{ctx: owned, cancel: cancel, kind: kind, header: hdr, guard: guard, done: make(chan guardedOpenResult, 1)}
	s.mu.Lock()
	if s.closed {
		err = s.err
	} else if len(s.guardedOpens) >= s.opt.maxOpen(s.side) {
		err = errors.New("link: the guarded open queue is full. Try: close a channel first")
	} else {
		s.guardedOpens = append(s.guardedOpens, request)
		signal(s.wake)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	select {
	case result := <-request.done:
		if err := ctx.Err(); err != nil {
			if result.stream != nil {
				_ = result.stream.Close()
			}
			return nil, err
		}
		return result.stream, result.err
	case <-ctx.Done():
	case <-s.done:
	}
	cancel()
	s.mu.Lock()
	taken := request.taken
	err = ctx.Err()
	if err == nil {
		err = s.err
	}
	s.mu.Unlock()
	if !taken {
		return nil, err
	}
	// There is no detached cleanup worker. The admitted caller and actual
	// writer both retain the source until the exact raw write completes.
	result := <-request.done
	if result.stream != nil {
		_ = result.stream.Close()
	}
	return nil, err
}

// takeGuardedOpen is called only by the physical writer, before build. The
// native guard never runs under s.mu; it cannot block Close's cancellation.
func (s *Session) takeGuardedOpen() (*guardedOpen, *Stream, func()) {
	for {
		s.mu.Lock()
		if s.closed || len(s.guardedOpens) == 0 {
			s.mu.Unlock()
			return nil, nil, nil
		}
		request := s.guardedOpens[0]
		s.guardedOpens[0] = nil
		s.guardedOpens = s.guardedOpens[1:]
		request.taken = true
		s.guardedOpening = request
		s.mu.Unlock()
		var release func()
		err := request.ctx.Err()
		if err == nil {
			release, err = request.guard(request.ctx)
		}
		if err == nil && release == nil {
			err = errors.New("link: source guard returned no held writer lifetime. Try: reconnect")
		}
		s.mu.Lock()
		if err == nil {
			err = request.ctx.Err()
		}
		if err == nil && s.closed {
			err = s.err
		}
		if err == nil && (s.count[s.side] >= s.opt.maxOpen(s.side) || request.kind == KindCtl && s.ctlOpen || s.nextID > math.MaxUint32) {
			err = errors.New("link: the guarded stream cannot enter its original admission slot. Try: close a channel or reconnect")
		}
		var stream *Stream
		if err == nil {
			id := uint32(s.nextID)
			s.nextID += 2
			s.ctlOpen = s.ctlOpen || request.kind == KindCtl
			stream = s.newStream(id, request.kind, request.header)
			s.add(stream)
			s.opens = append(s.opens, stream)
		} else {
			s.guardedOpening = nil
		}
		s.mu.Unlock()
		if err == nil {
			return request, stream, release
		}
		if release != nil {
			release()
		}
		request.cancel()
		request.done <- guardedOpenResult{err: err}
		// A refused source does not starve data or ordinary queued opens.
		s.mu.Lock()
		more := len(s.guardedOpens) != 0
		s.mu.Unlock()
		if more {
			signal(s.wake)
		}
		return nil, nil, nil
	}
}

func (s *Session) finishGuardedOpen(request *guardedOpen, stream *Stream, release func(), err error) {
	if request == nil {
		return
	}
	if request.stopWrite != nil && !request.stopWrite() {
		<-request.writeStopped
	}
	if release != nil {
		release()
	}
	s.mu.Lock()
	if s.guardedOpening == request {
		s.guardedOpening = nil
	}
	more := len(s.guardedOpens) != 0
	s.mu.Unlock()
	request.cancel()
	request.done <- guardedOpenResult{stream, err}
	if more {
		signal(s.wake)
	}
}

// Once an ID is admitted, cancellation must interrupt the actual indivisible
// raw write. Closing that carrier is the only way to revoke a blocked syscall;
// an unadmitted/refused request never arms this transport cancellation.
func (s *Session) armGuardedWrite(request *guardedOpen) {
	if request == nil {
		return
	}
	request.writeStopped = make(chan struct{})
	request.stopWrite = context.AfterFunc(request.ctx, func() { defer close(request.writeStopped); s.closeTransport() })
}

// Under s.mu. Cancel also wakes a source guard waiting for its native OP.
func (s *Session) cancelGuardedOpens() {
	for _, request := range s.guardedOpens {
		request.cancel()
	}
	if s.guardedOpening != nil {
		s.guardedOpening.cancel()
	}
}
