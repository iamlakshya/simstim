package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

const egressFrameWriteWait = 5 * time.Second

func egressFrameCancellation(ctx context.Context, expire func()) func() {
	changed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(changed)
		expire()
	})
	return func() {
		if !stop() {
			<-changed
		}
	}
}

// controlWriteTurn is leaf state beside the existing request waiter map.
// Waiting for its token never holds c.mu or another frame's deadline.
func (c *egressCtl) controlWriteTurn() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTurn == nil {
		c.writeTurn = make(chan struct{}, 1)
	}
	return c.writeTurn
}

// writeControlRequest serialises the same writer and the owned stream's
// deadline together. A queued cancellation cannot interrupt another frame.
func (c *egressCtl) writeControlRequest(ctx context.Context, frame proto.Request) (resultErr error) {
	if ctx == nil {
		return errors.New("egress control needs a request context")
	}
	payload, err := json.Marshal(frame)
	if err != nil {
		return errors.New("cannot encode egress control request")
	}
	if len(payload)+1 > proto.MaxFrame {
		return proto.ErrFrameTooBig
	}
	ctx, cancel := context.WithTimeout(ctx, egressFrameWriteWait)
	defer cancel()
	turn := c.controlWriteTurn()
	select {
	case turn <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("egress control stream is unavailable")
	}
	defer func() { <-turn }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.st == nil || c.w == nil || channelClosed(c.done) || (c.owner != nil && c.owner.dead.Load()) {
		return errors.New("egress control stream is unavailable")
	}
	deadline, _ := ctx.Deadline()
	if err := c.st.SetWriteDeadline(deadline); err != nil {
		c.closeOwnedControl()
		return errors.New("egress control stream is unavailable")
	}
	joinCancellation := egressFrameCancellation(ctx, func() {
		_ = c.st.SetWriteDeadline(time.Now())
	})
	defer func() {
		// Join a callback which has already started before handing deadline
		// ownership to a following ACK or ordinary control request.
		joinCancellation()
		if err := c.st.SetWriteDeadline(time.Time{}); err != nil {
			c.closeOwnedControl()
			if resultErr == nil {
				resultErr = errors.New("egress control stream is unavailable")
			}
		}
	}()
	if err := c.w.Write(proto.TRequest, payload); err != nil {
		// A partial frame cannot safely precede a following request. This
		// closes only the process/link captured by this control owner.
		c.closeOwnedControl()
		return errors.New("egress control request was not delivered")
	}
	return nil
}
