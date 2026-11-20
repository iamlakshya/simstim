package e2b

import (
	"context"
	"io"
	"sync"
	"time"
)

// slowBody is a StreamInput request's body that the fake takes at rate
// bytes a second. Over HTTP/1.1 it reads the body ahead into a buffer of
// 1 MiB, as the far end of a slow link does: the transport's reads go at the
// rate once the buffer is full, and 1 MiB is still buffered when the transport
// reads the pipe's EOF. Over HTTP/2 the server's own 1 MiB stream window is
// that buffer, so pacedBody paces reads of r.Body directly.
type slowBody struct {
	ctx  context.Context
	rate int
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	err  error // the end of the body, or its error
}

// rateBody returns body paced to rate bytes a second. When readAhead is set,
// it also buffers up to 1 MiB ahead of the reader.
func rateBody(ctx context.Context, body io.Reader, rate int, readAhead bool) io.Reader {
	if !readAhead {
		return &pacedBody{ctx: ctx, body: body, rate: rate}
	}
	s := &slowBody{ctx: ctx, rate: rate}
	s.cond = sync.NewCond(&s.mu)
	context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	go s.fill(body)
	return s
}

type pacedBody struct {
	ctx   context.Context
	body  io.Reader
	rate  int
	start time.Time
	total int64
}

func (p *pacedBody) Read(b []byte) (int, error) {
	if p.start.IsZero() {
		p.start = time.Now()
	}
	n, err := p.body.Read(b[:min(len(b), 4<<10)])
	if n > 0 {
		p.total += int64(n)
		due := p.start.Add(time.Duration(p.total) * time.Second / time.Duration(p.rate))
		if wait := time.Until(due); wait > 0 {
			select {
			case <-time.After(wait):
			case <-p.ctx.Done():
				return n, p.ctx.Err()
			}
		}
	}
	return n, err
}

// fill reads the body while the buffer has room.
func (s *slowBody) fill(body io.Reader) {
	b := make([]byte, 32<<10)
	for {
		s.mu.Lock()
		for len(s.buf) >= 1<<20 && s.ctx.Err() == nil {
			s.cond.Wait()
		}
		stop := s.ctx.Err() != nil
		s.mu.Unlock()
		if stop {
			return
		}
		n, err := body.Read(b)
		s.mu.Lock()
		s.buf = append(s.buf, b[:n]...)
		if err != nil {
			s.err = err
		}
		s.cond.Broadcast()
		s.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// Read takes at most 4 KiB, after the time that it takes at the rate.
func (s *slowBody) Read(p []byte) (int, error) {
	p = p[:min(len(p), 4<<10)]
	select {
	case <-time.After(time.Duration(len(p)) * time.Second / time.Duration(s.rate)):
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.buf) == 0 && s.err == nil && s.ctx.Err() == nil {
		s.cond.Wait()
	}
	if len(s.buf) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		return 0, s.ctx.Err()
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	s.cond.Broadcast()
	return n, nil
}
