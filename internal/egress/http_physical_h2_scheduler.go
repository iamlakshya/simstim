package egress

import (
	"context"
	"fmt"
	"github.com/iamlakshya/simstim/internal/api"
	"golang.org/x/net/http2"
	"io"
)

// The public pinned HTTP2 scheduler owns all ordinary frame selection. This
// wrapper observes its original stream retirement and frames already selected
// before retirement; it never creates a grant, schedules a private PING, or
// reads private scheduler fields.
type physicalH2Scheduler struct {
	http2.WriteScheduler
	owner *physicalH2Owner
}

func (s *physicalH2Scheduler) Pop() (http2.FrameWriteRequest, bool) {
	frame, ok := s.WriteScheduler.Pop()
	if ok && frame.StreamID() != 0 {
		s.owner.mu.Lock()
		if stream := s.owner.streams[frame.StreamID()]; stream != nil && !stream.finished {
			stream.selected++
		}
		s.owner.mu.Unlock()
	}
	return frame, ok
}

func (s *physicalH2Scheduler) CloseStream(id uint32) {
	// The original server calls this on its serve goroutine after cancelling
	// the stream and purging its queued writes. Already selected writes can
	// still be in its native writer or buffer and remain charged separately.
	s.WriteScheduler.CloseStream(id)
	s.owner.mu.Lock()
	var finish func()
	var cancel context.CancelFunc
	if stream := s.owner.streams[id]; stream != nil {
		stream.serverClosed = true
		if !stream.returned {
			// claim deliberately detached handler-return cancellation. A real
			// SDK reset while the handler is active must still stop that exact
			// original request and origin; normal returned handlers stay fenced.
			stream.reset, stream.cancelled, stream.resetRequested = true, true, true
			cancel = stream.cancel
		}
		finish = s.owner.finishLocked(stream)
	}
	s.owner.notifyLocked()
	s.owner.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if finish != nil {
		go finish()
	}
}

func (*physicalH2Scheduler) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }
