package link

// Done is closed when the stream is fully closed or reset, the peer no
// longer reads it, or the session ends. A peer write half-close leaves Done
// open so the caller can finish the request and send its response.
// Observing Done reads no bytes and changes no wire or stream state.
func (st *Stream) Done() <-chan struct{} {
	st.s.mu.Lock()
	defer st.s.mu.Unlock()
	if st.done == nil {
		st.done = make(chan struct{})
	}
	if st.s.closed || st.s.aborted || st.closedLocal || st.peerGone || st.gone {
		st.notifyDone()
	}
	return st.done
}

// notifyDone is called only under the existing session lifecycle mutex.
// Allocation is lazy, so streams with no observer keep their previous cost.
func (st *Stream) notifyDone() {
	if st.done != nil && !st.doneClosed {
		close(st.done)
		st.doneClosed = true
	}
}
