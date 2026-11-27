package supervisor

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
	"github.com/iamlakshya/simstim/internal/phaseclock"
)

const (
	inputHandshakeLimit       = 30 * time.Second
	inputIdleLimit            = 3 * time.Minute
	inputMinimumLimit         = 3 * time.Minute
	inputPlanningRate   int64 = 64 << 10
)

// inputAllocation rejects addition, ceiling and Duration overflow before max.
// This is finite planning time; no runtime throughput threshold is changed.
func inputAllocation(wireWork int64) (time.Duration, error) {
	if wireWork < 0 {
		return 0, inputFailure("the declared input work is negative")
	}
	seconds := wireWork / inputPlanningRate
	if wireWork%inputPlanningRate != 0 {
		seconds++
	}
	if seconds > (math.MaxInt64-int64(inputHandshakeLimit))/int64(time.Second) {
		return 0, inputFailure("the declared input deadline cannot be represented")
	}
	limit := inputHandshakeLimit + time.Duration(seconds)*time.Second
	if limit < inputMinimumLimit {
		limit = inputMinimumLimit
	}
	return limit, nil
}

type inputPhaseClock struct {
	ctx            context.Context
	cancel         context.CancelCauseFunc
	absoluteCancel context.CancelFunc
	absolute       time.Time
	handshake      time.Time
	mu             sync.Mutex
	timer          phaseclock.Timer
	epoch          uint64
	senderDone     bool
}

func newInputPhaseClock(parent context.Context, summary api.InputSummary) (*inputPhaseClock, error) {
	work, err := inputtransfer.WireWork(summary)
	if err != nil {
		return nil, err
	}
	limit, err := inputAllocation(work)
	if err != nil {
		return nil, err
	}
	owner, cancel := context.WithCancelCause(parent)
	admitted := phaseclock.Current.Now()
	deadline := admitted.Add(limit)
	ctx, absoluteCancel := phaseclock.WithDeadline(owner, deadline)
	clock := &inputPhaseClock{ctx: ctx, cancel: cancel, absoluteCancel: absoluteCancel, absolute: deadline, handshake: admitted.Add(inputHandshakeLimit)}
	clock.mu.Lock()
	clock.armLocked()
	clock.mu.Unlock()
	return clock, nil
}
func (c *inputPhaseClock) armLocked() {
	c.epoch++
	epoch := c.epoch
	if c.timer != nil {
		c.timer.Stop()
	}
	c.timer = phaseclock.Current.AfterFunc(inputIdleLimit, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.epoch == epoch && !c.senderDone {
			c.cancel(inputFailure("the input sender made no validated progress before its idle deadline"))
		}
	})
}
func (c *inputPhaseClock) progress(bytes int64) {
	if bytes <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.senderDone || c.ctx.Err() != nil {
		return
	}
	c.armLocked()
}
func (c *inputPhaseClock) senderEOF() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.senderDone {
		return
	}
	c.senderDone = true
	c.epoch++
	if c.timer != nil {
		c.timer.Stop()
	}
}
func (c *inputPhaseClock) close() { c.senderEOF(); c.absoluteCancel(); c.cancel(context.Canceled) }
