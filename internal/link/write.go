package link

import (
	"encoding/binary"
	"fmt"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

const (
	// maxData is the most bytes in one data frame, whose type byte and
	// stream ID come to maxFrame. A frame of a busy stream then waits
	// behind at most this much of another stream's.
	maxData = maxFrame - 1 - 4
	// maxBatch is about the most stream bytes in one write to the
	// connection.
	maxBatch = 256 << 10
)

// writeLoop sends this side's hello, then the frames of the streams, until
// the link ends. It is the only writer of the connection.
func (s *Session) writeLoop() {
	h := proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: s.side.String()}
	if err := proto.NewWriter(s.conn).WriteJSON(proto.THello, h); err != nil {
		s.fail(fmt.Errorf("link: send the hello: %w", err))
		return
	}
	for {
		select {
		case <-s.wake:
		case <-s.done:
			return
		}
		for {
			b, seq := s.build()
			if len(b) == 0 {
				break
			}
			if _, err := s.conn.Write(b); err != nil {
				s.fail(fmt.Errorf("link: write: %w", err))
				return
			}
			s.wrote(seq)
		}
	}
}

// wrote records that the connection has written batch seq, so a Close
// that waits for it returns.
func (s *Session) wrote(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = seq
	s.progress.wake()
}

// build makes the batch of frames to send next, into a buffer that the
// next batch reuses: the opens in the order of their IDs, credit and
// refusals, then one frame of each waiting stream in turn, ctl and holder
// streams first. It copies the bytes of data frames, so a stream's buffer
// is free again at once. seq numbers the batch.
func (s *Session) build() (b []byte, seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0
	}
	s.seq++
	b = s.wbuf[:0]
	for _, st := range s.opens {
		b = appendHead(b, proto.TStreamOpen, st.id, len(st.header))
		b = append(b, st.header...)
		// An open counts as sent from the moment that the connection may
		// show it to the other end, which can answer it at once.
		s.sentID = st.id
	}
	clear(s.opens)
	s.opens = s.opens[:0]
	for _, st := range s.grants {
		// The credit counts only from now, when its frame goes.
		b = appendHead(b, proto.TStreamWindow, st.id, 4)
		b = binary.LittleEndian.AppendUint32(b, uint32(st.grant)) //nolint:gosec // G115: at most Options.Window
		st.recvWin += st.grant
		st.grant, st.granting = 0, false
	}
	clear(s.grants)
	s.grants = s.grants[:0]
	if len(s.refusals) > 0 {
		for _, id := range s.refusals {
			b = appendHead(b, proto.TStreamClose, id, 1)
			b = append(b, closeRefused)
		}
		s.refusals = s.refusals[:0]
		s.drained.wake()
	}
	for start := len(b); len(b)-start < maxBatch; {
		st := s.pop()
		if st == nil {
			break
		}
		if st.sendable() {
			b = s.emit(st, b)
		}
		s.schedule(st)
	}
	s.wbuf = b
	return b, s.seq
}

// pop takes the next stream with frames to send. Under s.mu.
func (s *Session) pop() *Stream {
	q := &s.lo
	if len(s.hi) > 0 {
		q = &s.hi
	}
	if len(*q) == 0 {
		return nil
	}
	st := (*q)[0]
	(*q)[0] = nil
	*q = (*q)[1:]
	st.queued = false
	return st
}

// schedule queues st for the writer when it has a frame to send. Under
// s.mu.
func (s *Session) schedule(st *Stream) {
	if st.queued || !st.sendable() {
		return
	}
	st.queued = true
	if st.hi {
		s.hi = append(s.hi, st)
	} else {
		s.lo = append(s.lo, st)
	}
	signal(s.wake)
}

// sendable reports whether st has a data frame or a close to send now.
// Under s.mu.
func (st *Stream) sendable() bool {
	switch {
	case st.gone:
		return false
	case st.out.len() > 0:
		return st.sendCred > 0
	}
	return st.closeCode != closeNone
}

// emit appends st's next frame to b: its bytes as far as its credit goes,
// then its close. Under s.mu.
func (s *Session) emit(st *Stream, b []byte) []byte {
	st.lastSeq = s.seq
	if st.out.len() == 0 {
		b = appendHead(b, proto.TStreamClose, st.id, 1)
		b = append(b, byte(st.closeCode)) //nolint:gosec // G115: a close code from 0 to 2 here
		if st.closeCode != closeWrite {
			// The stream's slot is free from the moment that the
			// connection may show this close to the other end, which can
			// open another stream at once. Close still waits for the write.
			s.remove(st)
		}
		st.closeCode = closeNone
		return b
	}
	n := int(min(int64(st.out.len()), st.sendCred, maxData))
	b = appendHead(b, proto.TStreamData, st.id, n)
	b = st.out.appendTo(b, n)
	st.sendCred -= int64(n)
	st.writeQ.wake()
	return b
}

// giveCredit queues the credit for the bytes read from st since the last
// credit. A stream's credit waits in one sum, so it never takes more than
// one frame. Under s.mu.
func (s *Session) giveCredit(st *Stream) {
	if s.closed || st.gone {
		return
	}
	st.grant += st.consumed
	st.consumed = 0
	if !st.granting {
		st.granting = true
		s.grants = append(s.grants, st)
	}
	signal(s.wake)
}

// appendHead appends a stream frame's head: the frame's length, its type
// and the stream ID. rest is the size of the payload after the ID, which
// is at most maxData.
func appendHead(b []byte, t proto.Type, id uint32, rest int) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(1+4+rest)) //nolint:gosec // G115: rest is at most maxData
	b = append(b, byte(t))
	return binary.LittleEndian.AppendUint32(b, id)
}
