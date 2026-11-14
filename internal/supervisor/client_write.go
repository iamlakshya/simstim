package supervisor

import (
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// clientWriteWait bounds one complete frame's write, as frameStall bounds
// reading a frame from the guest. A stalled client must release the shared
// relay buffer so that another client can attach.
const clientWriteWait = 10 * time.Second

// write sends one frame to the client. Once a write fails, no later frame
// may follow its potentially partial bytes. A timeout closes the holder
// stream at once. Other failures keep the existing clean-input drain.
func (c *client) write(t proto.Type, p []byte) error {
	c.wmu.Lock()
	if c.writeErr != nil {
		err := c.writeErr
		c.wmu.Unlock()
		return err
	}
	wait := c.writeWait
	if wait <= 0 {
		wait = clientWriteWait
	}
	deadlineErr := c.conn.SetWriteDeadline(time.Now().Add(wait))
	err := deadlineErr
	if err == nil {
		err = c.w.Write(t, p)
		_ = c.conn.SetWriteDeadline(time.Time{})
	}
	c.writeErr = err
	c.wmu.Unlock()
	if err != nil {
		if deadlineErr != nil || errors.Is(err, os.ErrDeadlineExceeded) {
			c.closeStalledWrite()
		} else {
			c.closeSoon()
		}
	}
	return err
}

// closeStalledWrite gives a failed output no extra holder-stream linger.
// Normal clean input still uses closeSoon and its existing drain instead.
func (c *client) closeStalledWrite() {
	c.gmu.Lock()
	gs := c.gs
	c.gmu.Unlock()
	if gs != nil {
		_ = gs.SetWriteDeadline(time.Now())
	}
	c.close()
}
