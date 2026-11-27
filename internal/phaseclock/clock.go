// Package phaseclock supplies one clock for phase deadlines and their waiters.
package phaseclock

import (
	"context"
	"sync"
	"time"
)

// Timer owns one scheduled callback.
type Timer interface{ Stop() bool }

// Clock measures elapsed time and schedules deadline callbacks.
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type systemClock struct{}

func (systemClock) Now() time.Time                            { return time.Now() }
func (systemClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Current is the process clock. Tests may replace it before starting owners.
var Current Clock = systemClock{}

// WithDeadline uses the same clock for arithmetic and expiry.
func WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := Current.AfterFunc(deadline.Sub(Current.Now()), func() { cancel(context.DeadlineExceeded) })
	return deadlineContext{Context: ctx, deadline: deadline}, func() { timer.Stop(); cancel(context.Canceled) }
}

type deadlineContext struct {
	context.Context
	deadline time.Time
}

func (c deadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c deadlineContext) Err() error {
	if c.Context.Err() != nil {
		return context.Cause(c.Context)
	}
	return nil
}

// Sleep waits with this clock and remains interruptible.
func Sleep(ctx context.Context, d time.Duration) error {
	ready := make(chan struct{})
	var once sync.Once
	timer := Current.AfterFunc(d, func() { once.Do(func() { close(ready) }) })
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
