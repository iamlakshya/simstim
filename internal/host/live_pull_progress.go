package host

import (
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

type relayProgress struct {
	event api.PullOutputProgress
	valid func() bool
}

// SetProgressFactory binds actual responses to this bridge's pending read
// scopes before forwarding begins. It creates no request ID or export owner.
func (r *LivePullRelay) SetProgressFactory(factory func(proto.Response) func()) {
	r.mu.Lock()
	if !r.closed {
		r.progressFactory = factory
	}
	r.mu.Unlock()
}

// ReportProgress never performs I/O or calls validity while output holds its
// delivery turn. One latest fixed-size notice replaces the previous notice;
// one bounded worker revalidates before sending on the SAME retained line.
func (r *LivePullRelay) ReportProgress(event api.PullOutputProgress, valid func() bool) {
	if event.Validate() != nil {
		return
	}
	r.mu.Lock()
	if r.closed || valid == nil {
		r.mu.Unlock()
		return
	}
	if r.progressWake == nil {
		r.progressWake, r.progressQuit, r.progressDone = make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
		go r.sendProgress()
	}
	r.progressLatest = relayProgress{event: event, valid: valid}
	r.progressPending = true
	wake := r.progressWake
	r.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *LivePullRelay) sendProgress() {
	defer close(r.progressDone)
	var sent time.Time
	for {
		select {
		case <-r.progressQuit:
			return
		case <-r.progressWake:
		}
		r.mu.Lock()
		latest := r.progressLatest
		pending := r.progressPending
		r.progressPending = false
		r.progressLatest = relayProgress{}
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return
		}
		// No timer fabricates progress. A later REAL acknowledged piece is
		// needed to renew again after this rate interval.
		if !pending || time.Since(sent) < time.Second || !latest.valid() {
			continue
		}
		data, err := api.EncodePullOutputProgress(latest.event)
		if err != nil {
			r.cancel()
			return
		}
		r.send.Lock()
		// Routing never holds its mutex during Forward/retire I/O. Recheck
		// after the send turn so an admitted abort can revoke a queued notice.
		if !latest.valid() {
			r.send.Unlock()
			continue
		}
		r.mu.Lock()
		closed = r.closed
		r.mu.Unlock()
		if closed {
			r.send.Unlock()
			return
		}
		_ = r.line.c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err = r.line.w.Write(proto.TEvent, data)
		_ = r.line.c.SetWriteDeadline(time.Time{})
		r.send.Unlock()
		if err != nil {
			r.cancel()
			return
		}
		sent = time.Now()
	}
}

func (r *LivePullRelay) signalProgressStop() {
	r.mu.Lock()
	quit := r.progressQuit
	r.mu.Unlock()
	if quit != nil {
		r.progressStop.Do(func() { close(quit) })
	}
}

func (r *LivePullRelay) joinProgress() {
	r.mu.Lock()
	done := r.progressDone
	r.mu.Unlock()
	if done != nil {
		<-done
	}
}
