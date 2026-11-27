package link

import (
	"context"
	"net"
)

// WaitPeerGone waits for the peer to stop reading this same stream. A valid
// write half-close alone keeps it waiting. Nil requires an actual clean full
// peer close; reset, local/session revocation and cancellation return errors.
func (st *Stream) WaitPeerGone(ctx context.Context) error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if st.refused {
			return ErrRefused
		}
		if st.closedLocal || s.closed || s.aborted {
			if s.err != nil {
				return s.err
			}
			return net.ErrClosed
		}
		if st.peerGone {
			return nil
		}
		changed := st.writeQ.wait()
		s.mu.Unlock()
		select {
		case <-changed:
		case <-s.done:
		case <-ctx.Done():
		}
		s.mu.Lock()
	}
}
