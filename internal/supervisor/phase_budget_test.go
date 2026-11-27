package supervisor

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/phaseclock"
)

type logicalTimer struct {
	mu      sync.Mutex
	stopped bool
	at      time.Time
	fn      func()
}

func (t *logicalTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

type logicalClock struct {
	mu      sync.Mutex
	current time.Time
	timers  []*logicalTimer
}

func (c *logicalClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.current }
func (c *logicalClock) AfterFunc(d time.Duration, fn func()) phaseclock.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &logicalTimer{at: c.current.Add(d), fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}
func (c *logicalClock) advance(d time.Duration) {
	c.mu.Lock()
	end := c.current.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *logicalTimer
		for _, candidate := range c.timers {
			candidate.mu.Lock()
			live := !candidate.stopped && !candidate.at.After(end)
			candidate.mu.Unlock()
			if live && (next == nil || candidate.at.Before(next.at)) {
				next = candidate
			}
		}
		if next == nil {
			c.current = end
			c.mu.Unlock()
			return
		}
		c.current = next.at
		next.mu.Lock()
		next.stopped = true
		next.mu.Unlock()
		c.mu.Unlock()
		next.fn()
	}
}
func useLogicalClock(t *testing.T) *logicalClock {
	t.Helper()
	old := phaseclock.Current
	clock := &logicalClock{current: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	phaseclock.Current = clock
	t.Cleanup(func() { phaseclock.Current = old })
	return clock
}
func TestRemainingActiveBootBudgetSurvivesNearLimitStoreThenHolder(t *testing.T) {
	clock := useLogicalClock(t)
	budget := newActiveBootBudget(context.Background(), 30*time.Second)
	defer budget.close()
	clock.advance(time.Second)
	if err := budget.pause(); err != nil {
		t.Fatal(err)
	}
	store, cancel := phaseclock.WithDeadline(context.Background(), clock.Now().Add(memberStoreLimit))
	defer cancel()
	clock.advance(29 * time.Second)
	if store.Err() != nil || budget.Err() != nil {
		t.Fatal("near-limit Store spent normal boot time")
	}
	budget.resume()
	clock.advance(28 * time.Second)
	if budget.Err() != nil {
		t.Fatal("holder could not use remaining normal work budget")
	}
	clock.advance(time.Second)
	if !errors.Is(budget.Err(), context.DeadlineExceeded) {
		t.Fatal("active stages received another budget")
	}
}
func TestNonStoreBootStillExpiresAtOriginalBudget(t *testing.T) {
	clock := useLogicalClock(t)
	budget := newActiveBootBudget(context.Background(), 30*time.Second)
	defer budget.close()
	clock.advance(30 * time.Second)
	if !errors.Is(budget.Err(), context.DeadlineExceeded) {
		t.Fatal("stalled non-Store guest escaped original bound")
	}
}
func TestInputAllocationChecksDurationOverflowBeforeMinimum(t *testing.T) {
	for _, work := range []int64{-1, math.MaxInt64, 700_000_000_000_000} {
		if _, err := inputAllocation(work); err == nil {
			t.Fatal("unrepresentable deadline accepted")
		}
	}
	for _, work := range []int64{0, 1, 64 << 10} {
		if duration, err := inputAllocation(work); err != nil || duration != 3*time.Minute {
			t.Fatal("minimum input allocation changed")
		}
	}
	duration, err := inputAllocation(512 << 20)
	if err != nil || duration != 30*time.Second+8192*time.Second {
		t.Fatal("large slow-link allocation changed")
	}
}
func TestInputIdleOnlyPositiveProgressAndEOFDisarmsIdleNotAbsolute(t *testing.T) {
	clock := useLogicalClock(t)
	makeClock := func(absolute time.Duration) *inputPhaseClock {
		parent, cancel := context.WithCancelCause(context.Background())
		ctx, finish := phaseclock.WithDeadline(parent, clock.Now().Add(absolute))
		c := &inputPhaseClock{ctx: ctx, cancel: cancel, absoluteCancel: finish, absolute: clock.Now().Add(absolute)}
		c.mu.Lock()
		c.armLocked()
		c.mu.Unlock()
		return c
	}
	idle := makeClock(20 * time.Minute)
	defer idle.close()
	clock.advance(2 * time.Minute)
	idle.progress(0)
	clock.advance(time.Minute)
	if idle.ctx.Err() == nil {
		t.Fatal("zero-byte event refreshed idle")
	}
	active := makeClock(20 * time.Minute)
	defer active.close()
	for range 4 {
		clock.advance(2 * time.Minute)
		active.progress(1)
		if active.ctx.Err() != nil {
			t.Fatal("steady validated progress expired idle")
		}
	}
	active.senderEOF()
	clock.advance(4 * time.Minute)
	if active.ctx.Err() != nil {
		t.Fatal("sender idle remained armed during native clone/Finish")
	}
	clock.advance(8 * time.Minute)
	if !errors.Is(active.ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("EOF disarmed fixed absolute deadline")
	}
}
func TestInputProgressCannotExtendAbsoluteAndParentCancellationStaysImmediate(t *testing.T) {
	clock := useLogicalClock(t)
	parent, cancel := context.WithCancelCause(context.Background())
	ctx, finish := phaseclock.WithDeadline(parent, clock.Now().Add(5*time.Minute))
	c := &inputPhaseClock{ctx: ctx, cancel: cancel, absoluteCancel: finish}
	defer c.close()
	c.mu.Lock()
	c.armLocked()
	c.mu.Unlock()
	for range 4 {
		clock.advance(time.Minute)
		c.progress(1)
	}
	clock.advance(time.Minute)
	if !errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("regular progress extended absolute bound")
	}
	budgetParent, stop := context.WithCancel(context.Background())
	budget := newActiveBootBudget(budgetParent, 30*time.Second)
	defer budget.close()
	if err := budget.pause(); err != nil {
		t.Fatal(err)
	}
	stop()
	if !errors.Is(budget.Err(), context.Canceled) {
		t.Fatal("paused active budget detached from lifetime")
	}
}
