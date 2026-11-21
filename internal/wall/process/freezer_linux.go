//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

const processFreezeMaxV1 = 5 * time.Second

// freezerControlV1 is implemented only by the actual cgroup creator. Tests
// provide a private kernel model; no exported constructor accepts a callback,
// cgroup path, copied identifier or saved manifest as freezer authority.
type freezerControlV1 interface {
	check(bool) error
	write(bool) error
	state() (frozen, populated bool, err error)
	release() (bool, error)
}

type processFreezerV1 struct {
	mu         sync.Mutex
	control    freezerControlV1
	timer      *time.Timer
	deadline   time.Time
	generation uint64
	frozen     bool
	lost       bool
	closed     bool
}

func processFreezeErrorV1(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	wire := &proto.Error{Code: "freeze_off", Message: message, Hint: "Use a process sandbox started by the current helper in its delegated systemd user unit, then retry the fork."}
	if cause != nil {
		return errors.Join(wire, cause)
	}
	return wire
}

// FreezeV1 freezes every process created inside this runtime's own cgroup.
// Repeating it renews an unexpired lease. The kernel must confirm frozen=1
// before it succeeds; a process-group stop is never used as a substitute.
func (s *sandbox) FreezeV1(ctx context.Context, maxMS int) error {
	s.mu.Lock()
	freezer, reason := s.freezer, s.freezerOff
	live := !s.closed && s.cmd != nil && s.pidfd >= 0
	s.mu.Unlock()
	if freezer == nil || !live {
		if reason == "" {
			reason = "the original running process cgroup is unavailable"
		}
		return processFreezeErrorV1(reason, nil)
	}
	return freezer.freeze(ctx, maxMS)
}

// ThawV1 restores only a freeze owned by this runtime. A failure retains its
// original descriptors and retry timer until the kernel confirms frozen=0.
func (s *sandbox) ThawV1() error {
	s.mu.Lock()
	freezer := s.freezer
	s.mu.Unlock()
	if freezer == nil {
		return nil
	}
	return freezer.releaseLease()
}

// FreezeReasonV1 gives the actual unavailable capability's reason. It does
// not infer support from a wall name, saved row or systemd property alone.
func (s *sandbox) FreezeReasonV1() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.freezerOff
}

func (f *processFreezerV1) available() bool {
	if f == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.closed && f.control != nil && f.control.check(true) == nil
}

func (f *processFreezerV1) freeze(ctx context.Context, maxMS int) error {
	if f == nil || ctx == nil || maxMS < 1 || maxMS > int(processFreezeMaxV1/time.Millisecond) {
		return processFreezeErrorV1("a process freeze needs a lease between 1 and 5000 milliseconds", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.control == nil {
		return processFreezeErrorV1("the original process freezer has closed", nil)
	}
	if f.lost {
		return processFreezeErrorV1("the original process freeze lost continuity before renewal", nil)
	}
	if err := f.control.check(true); err != nil {
		return processFreezeErrorV1("the original process freezer changed", err)
	}
	if f.frozen && !time.Now().Before(f.deadline) {
		f.lost = true
		return processFreezeErrorV1("the previous process freeze expired before renewal", f.thawLocked())
	}
	frozen, _, err := f.control.state()
	if err != nil {
		return processFreezeErrorV1("cannot read the original cgroup freeze state", err)
	}
	if frozen != f.frozen {
		// An outside freeze is never adopted or thawed. A missing owned freeze
		// invalidates the copy, even if another freeze would succeed now.
		if f.frozen {
			f.lost = true
			f.endLeaseLocked()
		}
		return processFreezeErrorV1("the process cgroup freeze changed outside its original lease", nil)
	}
	f.deadline = time.Now().Add(time.Duration(maxMS) * time.Millisecond)
	f.generation++
	generation := f.generation
	if f.timer != nil {
		f.timer.Stop()
	}
	// Mark ownership before writing: a short write or error may still have
	// reached the kernel and must be restored before returning an error.
	f.frozen = true
	f.timer = time.AfterFunc(time.Until(f.deadline), func() { f.expire(generation) })
	if !frozen {
		err = f.control.write(true)
	}
	if err == nil {
		err = f.waitLocked(ctx, f.deadline, true, true)
	}
	if err != nil {
		f.lost = true
		return processFreezeErrorV1("the kernel did not confirm the original process freeze", errors.Join(err, f.thawLocked()))
	}
	return nil
}

func (f *processFreezerV1) waitLocked(ctx context.Context, until time.Time, want, live bool) error {
	for {
		if err := errors.Join(ctx.Err(), f.control.check(live)); err != nil {
			return err
		}
		frozen, _, err := f.control.state()
		if err != nil {
			return err
		}
		if frozen == want {
			if !time.Now().Before(until) {
				return context.DeadlineExceeded
			}
			return nil
		}
		remaining := time.Until(until)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		timer := time.NewTimer(min(remaining, time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (f *processFreezerV1) thawLocked() error {
	if !f.frozen {
		return nil
	}
	if f.control == nil || f.closed {
		return processFreezeErrorV1("the original frozen cgroup owner is unavailable", nil)
	}
	err := f.control.check(false)
	if err == nil {
		err = f.control.write(false)
	}
	if err == nil {
		err = f.waitLocked(context.Background(), time.Now().Add(processFreezeMaxV1), false, false)
	}
	if err != nil {
		f.retryThawLocked()
		return processFreezeErrorV1("the kernel has not confirmed the original cgroup thaw", err)
	}
	f.endLeaseLocked()
	return nil
}

func (f *processFreezerV1) releaseLease() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.thawLocked(); err != nil {
		return err
	}
	f.lost = false // An explicit owner release ends this operation's lease.
	return nil
}

func (f *processFreezerV1) endLeaseLocked() {
	f.generation++
	f.frozen = false
	f.deadline = time.Time{}
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
}

func (f *processFreezerV1) retryThawLocked() {
	if f.timer != nil {
		f.timer.Stop()
	}
	generation := f.generation
	f.timer = time.AfterFunc(100*time.Millisecond, func() { f.expire(generation) })
}

func (f *processFreezerV1) expire(generation uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.frozen || generation != f.generation {
		return
	}
	f.lost = true
	_ = f.thawLocked() // Failure keeps this same owner and its retry timer.
}

// closeEmpty joins the actual kernel emptiness check before removing only
// this creation. Timeout, Wait error and a closed link never release it.
func (f *processFreezerV1) closeEmpty() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	if err := f.thawLocked(); err != nil {
		return err
	}
	if f.control == nil {
		return fmt.Errorf("the original process cgroup has no native creator")
	}
	if err := f.control.check(false); err != nil {
		return err
	}
	_, populated, err := f.control.state()
	if err != nil || populated {
		return errors.Join(fmt.Errorf("the original process cgroup is not proved empty"), err)
	}
	released, err := f.control.release()
	f.closed = released
	return err
}
