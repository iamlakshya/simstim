package link

import (
	"context"
	"time"
)

// Flush waits until the connection has written all bytes accepted by Write
// before Flush takes the stream's writer lock. Later Writes wait until Flush
// returns. It does not wait for the peer to read those bytes, and it does not
// promise publication of an Open header without a Write.
// Like Write, it fails after Close or CloseWrite, or when the peer no longer
// accepts this stream's bytes.
//
// Cancellation ends only this wait. Bytes may still be queued or in flight.
// A caller that must revoke them must close its owned link before releasing
// the permission that protected the Write and Flush.
func (st *Stream) Flush(ctx context.Context) error {
	if err := st.lockFlush(ctx); err != nil {
		return err
	}
	defer st.wmu.Unlock()
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	var watermark uint64
	frozen := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if st.dropped {
			return ErrRefused
		}
		if err := st.writeErr(); err != nil {
			return err
		}
		if !frozen && st.out.len() == 0 {
			watermark, frozen = st.lastSeq, true
		}
		if frozen && s.written >= watermark {
			return nil
		}
		// Taking both channels while holding s.mu prevents a drain, close,
		// refusal or writer completion being lost between check and wait.
		write, progress := st.writeQ.wait(), s.progress.wait()
		s.mu.Unlock()
		select {
		case <-write:
		case <-progress:
		case <-s.done:
		case <-ctx.Done():
		}
		s.mu.Lock()
	}
}

// lockFlush never takes wmu while holding the session lock. A cancelled
// Flush can leave even when a Write holds wmu while it waits for credit.
func (st *Stream) lockFlush(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if st.wmu.TryLock() {
			return nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-st.s.done:
			timer.Stop()
			if err := ctx.Err(); err != nil {
				return err
			}
			return st.s.Err()
		case <-timer.C:
		}
	}
}
