package link

import "net"

// AbortStreams revokes every logical stream and wakes link waiters. It
// neither closes the transport nor joins raw I/O goroutines. The caller
// must own their lifetime, such as a dedicated process about to exit.
// Close retains its existing transport-close and join behaviour.
func (s *Session) AbortStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted = true
	s.cancelGuardedOpens()
	for _, stream := range s.accepts {
		stream.closedLocal, stream.writeShut = true, true
		s.remove(stream)
	}
	for _, stream := range s.streams {
		stream.closedLocal, stream.writeShut = true, true
		s.remove(stream)
	}
	s.accepts, s.opens, s.grants, s.refusals = nil, nil, nil, nil
	s.hi, s.lo = nil, nil
	signal(s.wake)
	s.acceptQ.wake()
	s.drained.wake()
	s.progress.wake()
	// Never mutate rbuf/wbuf: a raw syscall may still own their backing bytes.
	if !s.closed {
		s.closed, s.err = true, net.ErrClosed
		close(s.done)
	}
}
