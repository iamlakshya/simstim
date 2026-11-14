package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/phaseclock"
)

// activeBootBudget charges exactly one original budget across every normal
// stage. Only authenticated input and unique trusted Store phases may pause it.
type activeBootBudget struct {
	context.Context
	cancel    context.CancelCauseFunc
	parent    context.Context
	publisher *Supervisor
	mu        sync.Mutex
	remaining time.Duration
	entered   time.Time
	paused    bool
	timer     phaseclock.Timer
	epoch     uint64
}

func newActiveBootBudget(parent context.Context, limit time.Duration) *activeBootBudget {
	ctx, cancel := context.WithCancelCause(parent)
	b := &activeBootBudget{Context: ctx, cancel: cancel, parent: parent, remaining: limit}
	b.resume()
	return b
}
func (b *activeBootBudget) Err() error {
	if b.Context.Err() != nil {
		return context.Cause(b.Context)
	}
	return nil
}
func (b *activeBootBudget) Deadline() (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused {
		return time.Time{}, false
	}
	return b.entered.Add(b.remaining), true
}
func (b *activeBootBudget) pause() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused {
		return b.Err()
	}
	b.epoch++
	if b.timer != nil {
		b.timer.Stop()
	}
	b.remaining -= phaseclock.Current.Now().Sub(b.entered)
	b.paused = true
	if b.remaining <= 0 {
		b.cancel(context.DeadlineExceeded)
	}
	return b.Err()
}
func (b *activeBootBudget) resume() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paused = false
	b.entered = phaseclock.Current.Now()
	b.epoch++
	epoch := b.epoch
	b.timer = phaseclock.Current.AfterFunc(b.remaining, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.epoch == epoch && !b.paused {
			b.cancel(context.DeadlineExceeded)
		}
	})
}
func (b *activeBootBudget) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.epoch++
	if b.timer != nil {
		b.timer.Stop()
	}
	b.cancel(context.Canceled)
}
func (s *Supervisor) publishPhaseDeadline(deadline time.Time) error {
	s.mu.Lock()
	if s.rec.phase != host.LifeBooting {
		s.mu.Unlock()
		return nil
	}
	s.rec.bootBy = deadline
	snapshot := s.snapshot()
	s.mu.Unlock()
	return s.save(&snapshot, false)
}
func (s *Supervisor) publishActiveBudget(b *activeBootBudget) error {
	if b.publisher != nil {
		s = b.publisher
	}
	if err := b.Err(); err != nil {
		return err
	}
	deadline, _ := b.Deadline()
	return s.publishPhaseDeadline(deadline)
}
func (s *Supervisor) separatePhase(b *activeBootBudget, deadline time.Time, run func(context.Context) error) (err error) {
	if err = b.pause(); err != nil {
		return err
	}
	defer func() { b.resume(); err = errors.Join(err, s.publishActiveBudget(b)) }()
	publisher := s
	if b.publisher != nil {
		publisher = b.publisher
	}
	if err = publisher.publishPhaseDeadline(deadline); err != nil {
		return err
	}
	ctx, cancel := phaseclock.WithDeadline(b.parent, deadline)
	defer cancel()
	return run(ctx)
}
