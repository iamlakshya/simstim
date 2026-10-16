package client

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/keys"
)

// reconnect keeps trying to reach the session again. It returns detached=true
// if the user pressed the detach key meanwhile.
func (a *attacher) reconnect(ctx context.Context) (detached bool, err error) {
	a.res.Reconnects++
	// The title says so too, pushed so that it can be put back as it was.
	a.write([]byte("\x1b]9;4;3\x1b\\\x1b[22;0t\x1b]2;⧗ reconnecting · " + a.key + "\x1b\\"))
	defer func() {
		a.write([]byte("\x1b]9;4;0\x1b\\\x1b[23;0t"))
		if len(a.tracker.M.Title) > 0 {
			a.write(a.tracker.M.Title) // for terminals without a title stack
		}
	}()
	banner := time.After(bannerAfter)
	delay := time.Duration(0)
	for {
		if a.waitToRetry(ctx, banner, time.After(delay)) {
			return true, nil
		}
		if done, err := a.tryReconnect(ctx); done {
			return false, err
		}
		delay = nextRetry(delay)
	}
}

// waitToRetry waits for the next try: the retry delay, or a key press. It
// returns true when the user pressed the detach key, the terminal closed,
// or the attach was cancelled. The banner shows once, after a while.
func (a *attacher) waitToRetry(ctx context.Context, banner, retry <-chan time.Time) (detached bool) {
	for {
		select {
		case <-banner:
			a.flash("connection lost · reconnecting · keys are not sent · " + a.o.Settings.Attach.DetachKey + " to leave")
		case b, ok := <-a.inCh:
			if !ok {
				return true
			}
			a.tr.took()
			for _, p := range a.filter.Feed(b) {
				if p.Kind == keys.Match && p.Index == 0 && !p.Release && !p.Repeat {
					return true
				}
			}
			_ = a.filter.Flush()
			return false // a key press: try again now
		case <-retry:
			return false
		case <-ctx.Done():
			return true
		}
	}
}

// tryReconnect dials the host again and resumes the session. done is true
// when the attach goes on (err is nil) or must end with err; false means
// try again later.
func (a *attacher) tryReconnect(ctx context.Context) (done bool, err error) {
	a.ssh.ExitMaster(ctx)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, err := Dial(dctx, a.o.Host, false)
	cancel()
	var ue *UserError
	if err != nil {
		return errors.As(err, &ue), err
	}
	a.conn.Close()
	a.conn = conn
	cols, rows, xpx, ypx := a.size()
	before := a.cu.Offset
	att, err := a.handshake(a.cu.Offset, cols, rows, xpx, ypx, true)
	if err != nil {
		return errors.As(err, &ue), err
	}
	a.clearFlash()
	if att.Mode == "replay" {
		a.cu.History(att.Live)
	} else {
		a.begin(att, &AttachState{}, rows)
	}
	a.res.Missed += att.Live - before
	return true, nil
}

// nextRetry is the wait before the next try: now, then 1 s, doubling up to 30 s.
func nextRetry(delay time.Duration) time.Duration {
	if delay == 0 {
		return time.Second
	}
	return min(delay*2, 30*time.Second)
}
