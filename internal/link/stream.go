package link

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	// The byte of a close frame.
	closeBoth  = 0 // the sender neither writes nor reads any more
	closeWrite = 1 // the sender writes no more
	// closeRefused refuses an open, or resets a stream whose last bytes
	// could not go. The other end's reads then fail.
	closeRefused = 2
	closeNone    = -1

	// closeLinger is how long Close waits for the other end to take a
	// stream's queued bytes, when the stream has no write deadline.
	closeLinger = 5 * time.Second
)

// Stream is one stream. It is a net.Conn, and CloseWrite half-closes it.
type Stream struct {
	s      *Session
	id     uint32
	kind   Kind
	header []byte // the open's JSON header
	hi     bool   // ctl and holder streams go first

	wmu sync.Mutex // one Write at a time, so that writes never mix

	// Under s.mu. For a stream in the link, the window is always recvWin,
	// plus the bytes in in, plus consumed, plus grant.
	in, out     buffer // bytes received and not read; bytes written and not sent
	recvWin     int64  // the credit that the other end has, as sent to it
	consumed    int64  // bytes read since the last credit
	grant       int64  // credit to give, not yet handed to the writer
	sendCred    int64  // the bytes that this end may still send
	closeCode   int    // the close to send, or closeNone
	lastSeq     uint64 // the last batch that holds this stream's data or close, or 0
	queued      bool   // in s.hi or s.lo
	granting    bool   // in s.grants
	gone        bool   // no longer in s.streams
	remoteEOF   bool   // the other end writes no more
	peerGone    bool   // the other end reads no more
	refused     bool   // the other end refused or reset the stream
	dropped     bool   // that reset dropped bytes that waited here
	writeShut   bool   // this end writes no more
	closedLocal bool   // Close was called
	rdl, wdl    time.Time
	readQ       waitq // Reads that wait
	writeQ      waitq // a Write that waits
}

var _ net.Conn = (*Stream)(nil)

func (s *Session) newStream(id uint32, k Kind, hdr []byte) *Stream {
	w := int64(s.opt.Window)
	return &Stream{
		s: s, id: id, kind: k, header: hdr, hi: k == KindCtl || k == KindHolder,
		recvWin: w, sendCred: w, closeCode: closeNone,
	}
}

// Kind is what the stream carries.
func (st *Stream) Kind() Kind { return st.kind }

// Header decodes the open's JSON header into v. The header holds the field
// "kind" too.
func (st *Stream) Header(v any) error {
	if err := json.Unmarshal(st.header, v); err != nil {
		return fmt.Errorf("link: decode the header of a %s stream: %w", st.kind, err)
	}
	return nil
}

// Read reads the bytes that the other end sent. It returns io.EOF after
// the other end has closed its writes and every byte has been read, and
// ErrRefused when the other end refused or reset the stream.
func (st *Stream) Read(p []byte) (int, error) {
	s := st.s
	s.mu.Lock()
	for {
		n, done, err := st.tryRead(p)
		if done {
			s.mu.Unlock()
			return n, err
		}
		c, dl := st.readQ.wait(), st.rdl
		s.mu.Unlock()
		if err := s.await(c, dl); err != nil {
			return 0, err
		}
		s.mu.Lock()
	}
}

// tryRead reads what it can now. done is false when the Read must wait.
// Under s.mu.
func (st *Stream) tryRead(p []byte) (n int, done bool, err error) {
	switch {
	case st.closedLocal:
		return 0, true, net.ErrClosed
	case expired(st.rdl):
		return 0, true, os.ErrDeadlineExceeded
	case len(p) == 0:
		return 0, true, nil
	case st.in.len() > 0:
		n = st.in.read(p)
		st.consume(n)
		return n, true, nil
	case st.refused:
		return 0, true, ErrRefused
	case st.remoteEOF:
		return 0, true, io.EOF
	case st.s.closed:
		return 0, true, st.s.err
	}
	return 0, false, nil
}

// consume counts n bytes as read, and gives their credit back once half a
// window has been read. Under s.mu.
func (st *Stream) consume(n int) {
	st.consumed += int64(n)
	if !st.remoteEOF && st.consumed >= max(int64(st.s.opt.Window)/2, 1) {
		st.s.giveCredit(st)
	}
}

// Write queues p to be sent. It waits while a window's worth of bytes wait
// to be sent.
func (st *Stream) Write(p []byte) (int, error) {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	s := st.s
	limit := int(s.opt.Window)
	n := 0
	s.mu.Lock()
	for {
		err := st.writeErr()
		if err == nil && expired(st.wdl) {
			err = os.ErrDeadlineExceeded
		}
		if err != nil || len(p) == 0 {
			s.mu.Unlock()
			return n, err
		}
		if room := limit - st.out.len(); room > 0 {
			k := min(room, len(p))
			st.out.write(p[:k], limit)
			s.schedule(st)
			n += k
			p = p[k:]
			continue
		}
		c, dl := st.writeQ.wait(), st.wdl
		s.mu.Unlock()
		if err := s.await(c, dl); err != nil {
			return n, err
		}
		s.mu.Lock()
	}
}

// writeErr is why a Write fails now, or nil. Under s.mu.
func (st *Stream) writeErr() error {
	switch {
	case st.closedLocal:
		return net.ErrClosed
	case st.writeShut:
		return fmt.Errorf("link: write after CloseWrite: %w", net.ErrClosed)
	case st.refused:
		return ErrRefused
	case st.peerGone:
		return ErrPeerClosed
	case st.s.closed:
		return st.s.err
	}
	return nil
}

// CloseWrite tells the other end that this end writes no more, after the
// bytes already written. The other end's reads then end with io.EOF.
func (st *Stream) CloseWrite() error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case st.closedLocal:
		return net.ErrClosed
	case s.closed:
		return s.err
	case st.writeShut:
		return nil
	}
	st.writeShut = true
	st.writeQ.wake()
	if !st.peerGone {
		st.closeCode = closeWrite
		s.schedule(st)
	}
	return nil
}

// Close ends the stream both ways. It sends the bytes already written,
// then the close, and returns nil once the connection has written them.
// When the other end closes both ways first, the bytes that wait are
// dropped, and Close returns nil. When it resets the stream and so drops
// bytes, Close returns ErrRefused.
// It waits for the write deadline when one is set, or for 5 s. When the
// other end has not taken every byte by then, Close resets the stream, so
// that the other end's reads fail, and returns an error. When the bytes
// still wait for the connection, it returns an error too. Bytes that
// arrive after Close are dropped.
func (st *Stream) Close() error {
	s := st.s
	s.mu.Lock()
	if st.closedLocal {
		s.mu.Unlock()
		return net.ErrClosed
	}
	st.closedLocal, st.writeShut = true, true
	// Nobody reads these bytes now. Their credit goes back, so that an
	// other end that still writes is not stuck.
	st.consume(st.in.len())
	st.in.reset()
	st.readQ.wake()
	st.writeQ.wake()
	switch {
	case st.peerGone:
		// The other end closed both ways first, and dropped what waited.
		// A reset that dropped bytes is an error.
		s.remove(st)
		s.mu.Unlock()
		if st.dropped {
			return ErrRefused
		}
		return nil
	case s.closed:
		// Bytes that wait, or that are in a batch that the connection never
		// finished writing, are lost with the link.
		lost := st.out.len() > 0 || st.lastSeq > s.written
		s.remove(st)
		err := s.err
		s.mu.Unlock()
		if lost {
			return err
		}
		return nil
	}
	st.closeCode = closeBoth
	s.schedule(st)
	dl := st.wdl
	if dl.IsZero() {
		dl = time.Now().Add(closeLinger)
	}
	s.mu.Unlock()
	return st.linger(dl)
}

// linger waits until the connection has written the stream's close, or
// until dl. Then it resets a stream whose bytes the other end has not
// taken, and reports bytes that are still on their way.
func (st *Stream) linger(dl time.Time) error {
	s := st.s
	t := time.NewTimer(time.Until(dl))
	defer t.Stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if done, err := st.closed(); done {
			return err
		}
		c := s.progress.wait()
		s.mu.Unlock()
		select {
		case <-c:
		case <-s.done:
		case <-t.C:
			s.mu.Lock()
			return st.giveUp()
		}
		s.mu.Lock()
	}
}

// closed reports whether a Close is over, and with what error: the stream
// has left the link and the connection has written every batch that holds
// its data or its close, or the link has ended. Under s.mu.
func (st *Stream) closed() (bool, error) {
	switch {
	case st.gone && st.lastSeq <= st.s.written:
		if st.dropped {
			return true, ErrRefused
		}
		return true, nil
	case st.s.closed:
		return true, st.s.err
	}
	return false, nil
}

// giveUp ends a Close that waited too long. Under s.mu.
func (st *Stream) giveUp() error {
	s := st.s
	if done, err := st.closed(); done {
		return err
	}
	if lost := st.out.len(); lost > 0 {
		st.out.reset()
		st.closeCode = closeRefused
		s.schedule(st)
		return fmt.Errorf("link: the other end had not taken the last %d bytes of a %s stream when Close gave up, so the stream was reset. Try: read the stream at the other end, or set a later write deadline", lost, st.kind)
	}
	// The bytes and the close wait for the connection. They still go.
	return fmt.Errorf("link: the last bytes and the close of a %s stream had not reached the connection when Close gave up. Try: check that the other end reads the link, or set a later write deadline", st.kind)
}

// remove takes a stream out of the link and out of the writer's queues,
// and frees its buffers. Under s.mu.
func (s *Session) remove(st *Stream) {
	if st.gone {
		return
	}
	st.gone = true
	delete(s.streams, st.id)
	s.count[ownerOf(st.id)]--
	if st.queued {
		s.hi, s.lo = without(s.hi, st), without(s.lo, st)
		st.queued = false
	}
	if st.granting {
		s.grants = without(s.grants, st)
		st.granting = false
	}
	// An open waits here only when the link has ended. While the link is
	// up, a stream leaves only after its open has gone.
	s.opens = without(s.opens, st)
	st.grant = 0
	st.in.reset()
	st.out.reset()
	st.closeCode = closeNone
	s.progress.wake()
	st.readQ.wake()
	st.writeQ.wake()
}

// without removes st from q.
func without(q []*Stream, st *Stream) []*Stream {
	for i, x := range q {
		if x == st {
			copy(q[i:], q[i+1:])
			q[len(q)-1] = nil
			return q[:len(q)-1]
		}
	}
	return q
}

// expired reports whether the deadline dl has passed. The zero time never
// passes.
func expired(dl time.Time) bool { return !dl.IsZero() && !time.Now().Before(dl) }

// await waits for c, the deadline dl, or the end of the link.
func (s *Session) await(c <-chan struct{}, dl time.Time) error {
	var timeout <-chan time.Time
	if !dl.IsZero() {
		d := time.Until(dl)
		if d <= 0 {
			return os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-c:
	case <-s.done:
	case <-timeout:
		return os.ErrDeadlineExceeded
	}
	return nil
}

// SetDeadline sets the read and write deadlines.
func (st *Stream) SetDeadline(t time.Time) error {
	if err := st.SetReadDeadline(t); err != nil {
		return err
	}
	return st.SetWriteDeadline(t)
}

// SetReadDeadline sets the time after which a Read fails with
// os.ErrDeadlineExceeded. The zero time means no deadline.
func (st *Stream) SetReadDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if err := st.deadlineErr(); err != nil {
		return err
	}
	st.rdl = t
	st.readQ.wake()
	return nil
}

// SetWriteDeadline sets the time after which a Write fails with
// os.ErrDeadlineExceeded. The zero time means no deadline. It also bounds
// the wait of Close.
func (st *Stream) SetWriteDeadline(t time.Time) error {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if err := st.deadlineErr(); err != nil {
		return err
	}
	st.wdl = t
	st.writeQ.wake()
	return nil
}

// deadlineErr is why a deadline cannot be set. Under s.mu.
func (st *Stream) deadlineErr() error {
	switch {
	case st.closedLocal:
		return net.ErrClosed
	case st.s.closed:
		return st.s.err
	}
	return nil
}

// Addr is the address of one end of a stream.
type Addr struct {
	Side Side
	Kind Kind
	ID   uint32
}

// Network is "link".
func (Addr) Network() string { return "link" }

// String is the side, the kind and the stream ID, such as "guest/egress/4".
func (a Addr) String() string { return fmt.Sprintf("%s/%s/%d", a.Side, a.Kind, a.ID) }

// LocalAddr is this end of the stream.
func (st *Stream) LocalAddr() net.Addr { return Addr{st.s.side, st.kind, st.id} }

// RemoteAddr is the other end of the stream.
func (st *Stream) RemoteAddr() net.Addr { return Addr{st.s.side.other(), st.kind, st.id} }
