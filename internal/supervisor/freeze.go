package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// outsideFreezerV1 is implemented only by the original process runtime's
// cgroup creator. It freezes all sandbox members, including new process groups.
type outsideFreezerV1 interface {
	FreezeV1(context.Context, int) error
	ThawV1() error
}

var freezeRenewEveryV1 = time.Duration(proto.MaxFreezeMS) * time.Millisecond / 3

func snapshotFailureV1(message string, cause error) error {
	if cause != nil {
		message += ": " + cause.Error()
	}
	return &proto.Error{Code: "freeze", Message: message, Hint: "Retry on the original running sandbox after checking its disk and runtime."}
}

func (s *Supervisor) acquireSnapshotV1(ctx context.Context) (func(), error) {
	s = s.owner()
	s.snapshotMu.Lock()
	if s.snapshotTurn == nil {
		s.snapshotTurn = make(chan struct{}, 1)
	}
	turn := s.snapshotTurn
	s.snapshotMu.Unlock()
	select {
	case turn <- struct{}{}:
		return func() { <-turn }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.lifetimeContext().Done():
		return nil, context.Cause(s.lifetimeContext())
	}
}

// The lease exists only inside the original supervisor's snapshot turn. Its
// callbacks close over the actual native runtime or prepared guest stream.
// It cannot be rebuilt from a deadline, acknowledgement or request data.
type snapshotFreezeLeaseV1 struct {
	mu          sync.Mutex
	owner       *Supervisor
	ctx         context.Context
	cancel      context.CancelCauseFunc
	freeze      func(context.Context) error
	lastStarted time.Time
	started     bool
	closed      bool
	failure     error
}

func (lease *snapshotFreezeLeaseV1) renewForHandoffV1(ctx context.Context) error {
	if lease == nil || ctx == nil {
		return snapshotFailureV1("the handoff has no original physical freeze", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.owner == nil || lease.ctx == nil || lease.cancel == nil || lease.freeze == nil || lease.closed || lease.failure != nil {
		return errors.Join(snapshotFailureV1("the original physical freeze ended before handoff", nil), lease.failure)
	}
	if err := errors.Join(ctx.Err(), lease.ctx.Err()); err != nil {
		return err
	}
	const validity = time.Duration(proto.MaxFreezeMS) * time.Millisecond
	started := time.Now()
	if lease.started && started.Sub(lease.lastStarted) >= validity {
		lease.failure = snapshotFailureV1("the source freeze expired before renewal", nil)
	} else {
		renew, done := context.WithTimeout(ctx, time.Second)
		lease.failure = lease.freeze(renew)
		done()
		if lease.failure == nil && (time.Since(started) >= validity || lease.started && time.Since(lease.lastStarted) >= validity) {
			lease.failure = snapshotFailureV1("the source freeze expired while renewal waited", nil)
		}
	}
	if lease.failure != nil {
		lease.cancel(lease.failure)
		return lease.failure
	}
	lease.lastStarted, lease.started = started, true
	return nil
}

func (lease *snapshotFreezeLeaseV1) checkV1() error {
	if lease == nil {
		return snapshotFailureV1("the copy has no original freeze lease", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.owner == nil || lease.ctx == nil || !lease.started || lease.closed {
		return snapshotFailureV1("the original physical freeze is not active", nil)
	}
	if lease.failure != nil {
		return lease.failure
	}
	if err := lease.ctx.Err(); err != nil {
		return err
	}
	if time.Since(lease.lastStarted) >= time.Duration(proto.MaxFreezeMS)*time.Millisecond {
		return snapshotFailureV1("the copy outlasted its last source freeze", nil)
	}
	return nil
}

// withFrozenV1 holds ONE snapshot turn and renews the SAME physical freeze.
// A renewal past its previous deadline is failure even if a later freeze
// succeeds: files may already have changed in between. Work must honour ctx.
func (s *Supervisor) withFrozenV1(ctx context.Context, work func(context.Context) error) error {
	if work == nil {
		return snapshotFailureV1("the snapshot has no original worker", nil)
	}
	return s.withFrozenLeaseV1(ctx, func(lease *snapshotFreezeLeaseV1) error { return work(lease.ctx) })
}

// A current-copy worker may retain this SAME private lease while the outside
// original owner completes a physical all-writer freeze handoff. It must not
// return from work before that handoff or its actual cancel/thaw join.
func (s *Supervisor) withFrozenLeaseV1(ctx context.Context, work func(*snapshotFreezeLeaseV1) error) (returned error) {
	s = s.owner()
	if ctx == nil || work == nil {
		return snapshotFailureV1("the snapshot has no original operation", nil)
	}
	release, err := s.acquireSnapshotV1(ctx)
	if err != nil {
		return err
	}
	defer release()
	owned, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	stopLifetime := context.AfterFunc(s.lifetimeContext(), func() { cancel(context.Cause(s.lifetimeContext())) })
	defer stopLifetime()
	s.mu.Lock()
	runtime, ctl := s.sb, s.ctl
	micro := s.m != nil && s.m.Wall != nil && s.m.Wall.Level == "microvm"
	s.mu.Unlock()
	var freeze func(context.Context) error
	var thaw func() error
	if micro && ctl != nil {
		freeze = func(ctx context.Context) error {
			return ctl.request(ctx, proto.CtlFreeze, proto.CtlFreezeArgs{MaxMS: proto.MaxFreezeMS}, nil)
		}
		thaw = func() error {
			cleanup, done := context.WithTimeout(context.Background(), time.Second)
			defer done()
			return ctl.requestDetached(cleanup, proto.CtlThaw, struct{}{}, nil)
		}
	} else if physical, ok := runtime.(outsideFreezerV1); ok {
		freeze = func(ctx context.Context) error { return physical.FreezeV1(ctx, proto.MaxFreezeMS) }
		thaw = physical.ThawV1
	} else {
		return &proto.Error{Code: "freeze_off", Message: "the original runtime cannot freeze all sandbox writers", Hint: "Use a delegated process wall or a microvm with its original runtime owner."}
	}
	lease := &snapshotFreezeLeaseV1{owner: s, ctx: owned, cancel: cancel, freeze: freeze}
	err = lease.renewForHandoffV1(owned)
	if err != nil {
		return errors.Join(snapshotFailureV1("cannot freeze the source sandbox", err), thaw())
	}
	defer func() {
		lease.mu.Lock()
		lease.closed = true
		lease.mu.Unlock()
		returned = errors.Join(returned, thaw())
		if !micro {
			s.mu.Lock()
			if !s.heartbeatPause.IsZero() {
				s.heartbeatDelay += now().Sub(s.heartbeatPause)
				s.heartbeatPause = time.Time{}
			}
			s.mu.Unlock()
		}
	}()
	if !micro {
		s.mu.Lock()
		s.heartbeatPause = now()
		s.mu.Unlock()
	}
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	go func() {
		defer close(renewalDone)
		ticker := time.NewTicker(freezeRenewEveryV1)
		defer ticker.Stop()
		for {
			select {
			case <-stopRenewal:
				return
			case <-owned.Done():
				return
			case <-ticker.C:
				if lease.renewForHandoffV1(owned) != nil {
					return
				}
			}
		}
	}()
	err = work(lease)
	close(stopRenewal)
	<-renewalDone
	return errors.Join(err, lease.checkV1(), context.Cause(owned))
}
