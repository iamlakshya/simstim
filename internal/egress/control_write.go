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
	return s.writeControlPayloadOwned(ctx, writer, kind, payload)
}

// The caller owns ctlMu. Success includes the actual underlying link write;
// Stream.Write alone only queues bytes. Failed or cancelled partial frames
// close and physically join the same owned session before this turn ends.
func (s *Server) writeControlPayloadOwned(ctx context.Context, writer *proto.Writer, kind proto.Type, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stream := s.ctlStream
	if stream == nil {
		return writer.Write(kind, payload)
	}
	deadline, _ := ctx.Deadline()
	if err := stream.SetWriteDeadline(deadline); err != nil {
		_ = s.sess.Close()
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writer.Write(kind, payload); err != nil {
		_ = s.sess.Close()
		return errors.New("egress control frame was not delivered")
	}
	if err := stream.Flush(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			drainCtx, drainCancel := context.WithTimeout(context.WithoutCancel(ctx), 200*time.Millisecond)
			drainErr := stream.Flush(drainCtx)
			drainCancel()
			if drainErr == nil {
				return nil
			}
		}
		_ = s.sess.Close()
		return errors.New("egress control frame was not delivered")
	}
	return nil
}
