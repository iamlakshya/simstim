package egress

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

const controlWriteTimeout = 5 * time.Second

// writeControlJSON owns both the complete frame and its stream deadline.
// A cancelled queued response can leave without waiting for another writer.
// No callback may change a deadline after ownership passes to the next frame.
func (s *Server) writeControlJSON(ctx context.Context, writer *proto.Writer, kind proto.Type, value any) error {
	if ctx == nil {
		return errors.New("egress control has no request context")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode egress control frame")
	}
	if len(payload)+1 > proto.MaxFrame {
		return proto.ErrFrameTooBig
	}
	ctx, cancel := context.WithTimeout(ctx, controlWriteTimeout)
	defer cancel()
	if err := s.ctlMu.LockContext(ctx); err != nil {
		return err
	}
	defer s.ctlMu.Unlock()
	if writer == nil {
		writer = s.ctl
	}
	if writer == nil || (s.ctlStream != nil && writer != s.ctl) {
		return errors.New("egress control stream is unavailable")
	}
	stream := s.ctlStream
	if stream == nil {
		// Embedded callers can provide an in-memory control writer. The
		// actual control handler always binds its owned link stream first.
		return writer.Write(kind, payload)
	}
	deadline, _ := ctx.Deadline()
	if err := stream.SetWriteDeadline(deadline); err != nil {
		_ = stream.Close()
		return errors.New("egress control stream is unavailable")
	}
	changed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(changed)
		_ = stream.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-changed
		}
		_ = stream.SetWriteDeadline(time.Time{})
	}()
	if err := writer.Write(kind, payload); err != nil {
		// A partial control frame cannot be followed by another frame.
		// Closing this owned stream makes the control reader stop the link.
		_ = stream.Close()
		return errors.New("egress control frame was not delivered")
	}
	return nil
}
