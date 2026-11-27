package client

import (
	"context"
	"errors"
	"time"
)

type hostAttemptPhaseV1 uint8
type hostAttemptRetryV1 uint8

const (
	hostAttemptLocalV1 hostAttemptPhaseV1 = iota
	hostAttemptDialV1
	hostAttemptNewV1
)

const (
	hostAttemptNoRetryV1 hostAttemptRetryV1 = iota
	hostAttemptDialRetryV1
	hostAttemptNoRoomRetryV1
	hostAttemptReservationRetryV1
	hostAttemptSettledRetryV1
	hostAttemptFactsRetryV1
)

// Only the native call site creates this result. A wrapped copy, matching code
// or another attempt's result is not this exact returned object.
type hostAttemptFailureV1 struct {
	attempt          *HostAttemptV1
	actual           error
	phase            hostAttemptPhaseV1
	kind             hostAttemptRetryV1
	reservation      *newSecretPlan
	placementRefusal *placementReservationRefusalV1
	settledRefusal   *settledNewRefusalV1
	factsRefusal     *refreshedPlacementRefusalV1
}

func (e *hostAttemptFailureV1) Error() string { return e.actual.Error() }
func (e *hostAttemptFailureV1) Unwrap() error { return e.actual }

func (a *HostAttemptV1) recordFailureV1(actual error, phase hostAttemptPhaseV1) error {
	if actual == nil {
		return nil
	}
	failure := &hostAttemptFailureV1{attempt: a, actual: actual, phase: phase}
	if phase == hostAttemptDialV1 && eligibleDialFailureV1(actual) {
		failure.kind = hostAttemptDialRetryV1
	}
	if phase == hostAttemptNewV1 {
		if proof := actualSettledNewRefusalV1(actual, a); proof != nil {
			failure.kind, failure.settledRefusal = hostAttemptSettledRetryV1, proof
		}
		var user *UserError
		// Classification is frozen now: UserError is publicly mutable.
		if errors.As(actual, &user) && user.Code == "no_room" {
			a.request.mu.Lock()
			reservation := a.request.newSecrets
			a.request.mu.Unlock()
			if pristineAttemptReservationV1(reservation, a.conn) {
				failure.kind, failure.reservation = hostAttemptNoRoomRetryV1, reservation
			}
		}
	}
	if phase == hostAttemptLocalV1 && a.plan != nil && a.plan.automatic != nil {
		var facts *refreshedPlacementRefusalV1
		if errors.As(actual, &facts) && facts.plan == a.plan.automatic && facts.candidate == a.candidate &&
			facts.observation != nil && a.candidate != nil && a.candidate.observation == facts.observation &&
			a.conn != nil && SameEntry(a.conn.Host, facts.observation.host) && a.conn.HostID() == facts.observation.hostID &&
			a.placement == nil && !a.newCalled {
			failure.kind, failure.factsRefusal = hostAttemptFactsRetryV1, facts
		}
		var refusal *placementReservationRefusalV1
		if errors.As(actual, &refusal) && refusal.store == a.plan.automatic.store &&
			refusal.candidate == a.candidate && a.placement == nil && !a.newCalled {
			failure.kind, failure.placementRefusal = hostAttemptReservationRetryV1, refusal
		}
	}
	a.mu.Lock()
	a.failure = failure
	a.mu.Unlock()
	return failure
}

// Public error metadata is only a negative/eligibility filter. Actual retry
// still requires the final retained native process's joined Wait below.
func eligibleDialFailureV1(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var user *UserError
	if errors.As(err, &user) {
		return user.Code == "unreachable" && user.Kind != KindRefused
	}
	return errors.Is(err, ErrUnreachable)
}

// NewSession is the only start result this plan can use as retry evidence.
// Confirmation questions can call it again on this same attempt/request.
func (a *HostAttemptV1) NewSession(ctx context.Context, args NewArgs) (*Session, error) {
	if a == nil || a.plan == nil || ctx == nil {
		return nil, grantOperationError()
	}
	p := a.plan
	p.mu.Lock()
	a.mu.Lock()
	if p.active != a || p.closed || !a.ready || a.closing || a.started || a.running != nil ||
		a.ctx == nil || a.conn == nil || a.request == nil {
		a.mu.Unlock()
		p.mu.Unlock()
		return nil, grantOperationError()
	}
	a.running, a.failure = make(chan struct{}), nil
	original, conn, request := a.ctx, a.conn, a.request
	a.mu.Unlock()
	p.mu.Unlock()
	defer a.endOperationV1()
	if before := grantRequestOf(ctx); before != nil && before != request {
		return nil, a.recordFailureV1(grantOperationError(), hostAttemptLocalV1)
	}
	call, end, err := narrowHostAttemptContextV1(original, ctx)
	if err != nil {
		return nil, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	defer end()
	request.mu.Lock()
	valid := request.hostSelection == p.selection && request.selectedHost == a.host.Name &&
		request.Untrusted == p.need.Untrusted && request.SSHAgent == p.args.SSHAgent &&
		request.purpose == grantRemote && request.operation == grantOpNew
	request.mu.Unlock()
	if !valid || grantRequestOf(call) != request || args.SSHAgent != p.args.SSHAgent {
		return nil, a.recordFailureV1(grantOperationError(), hostAttemptLocalV1)
	}
	if err := a.checkOriginalIntentV1(call, args); err != nil {
		return nil, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	if p.automatic != nil {
		args, err = p.automatic.placementArgumentsV1(call, a, args)
		if err != nil {
			return nil, a.recordFailureV1(err, hostAttemptLocalV1)
		}
	}
	a.mu.Lock()
	a.newCalled = true
	a.mu.Unlock()
	call, err = hostAttemptNewPublicationContextV1(call, a)
	if err != nil {
		return nil, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	session, err := conn.NewSession(call, args)
	if err != nil {
		return session, a.recordFailureV1(err, hostAttemptNewV1)
	}
	a.mu.Lock()
	a.started = true
	placement := a.placement
	a.mu.Unlock()
	if placement != nil {
		// A failed accounting update cannot undo a session or permit a retry.
		if err := placement.finish(call, session.ID, false); err != nil {
			return session, a.recordFailureV1(err, hostAttemptLocalV1)
		}
	}
	return session, nil
}

func (a *HostAttemptV1) endOperationV1() {
	a.mu.Lock()
	done := a.running
	a.running = nil
	a.mu.Unlock()
	if done != nil {
		close(done)
	}
}

// Retry retires the active actual attempt before permitting Next. false,nil
// means stop and report its original error. Cleanup uncertainty is an error;
// the plan retains the same request, connection owners and result for a join.
func (p *HostAttemptPlanV1) Retry(ctx context.Context, a *HostAttemptV1, returned error) (bool, error) {
	if p == nil || ctx == nil || a == nil {
		return false, grantOperationError()
	}
	p.mu.Lock()
	a.mu.Lock()
	failure, exact := returned.(*hostAttemptFailureV1)
	valid := exact && failure != nil && failure == a.failure && failure.attempt == a &&
		p.active == a && a.plan == p && !p.closed && !a.started && a.running == nil && p.ctx != nil
	if !valid {
		a.mu.Unlock()
		p.mu.Unlock()
		return false, grantOperationError()
	}
	ownerErr := p.ctx.Err()
	a.mu.Unlock()
	p.mu.Unlock()
	if ownerErr != nil {
		return false, ownerErr
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if failure.kind == hostAttemptNoRetryV1 {
		return false, nil
	}
	if failure.kind == hostAttemptNoRoomRetryV1 && !a.sameReservationIdentityV1(failure.reservation) {
		return false, nil
	}
	if failure.kind == hostAttemptReservationRetryV1 && (p.automatic == nil ||
		failure.placementRefusal == nil || failure.placementRefusal.store != p.automatic.store ||
		failure.placementRefusal.candidate != a.candidate || a.placement != nil || a.newCalled) {
		return false, grantOperationError()
	}
	result, err := a.joinCleanupV1(ctx)
	if err != nil {
		return false, err
	}
	if !result.absent {
		return false, hostAttemptUncertainV1()
	}
	if failure.kind == hostAttemptSettledRetryV1 && result.settledRefusal != failure.settledRefusal {
		return false, hostAttemptUncertainV1()
	}
	if failure.kind == hostAttemptDialRetryV1 && !nativeAttemptUnreachableV1(a.dial) {
		return false, nil
	}
	if failure.kind == hostAttemptNoRoomRetryV1 && (!result.retired || result.reservation != failure.reservation) {
		return false, hostAttemptUncertainV1()
	}
	// Source changes never silently renew the outer request on another host.
	check, end, err := narrowHostAttemptContextV1(p.ctx, ctx)
	if err != nil {
		return false, err
	}
	defer end()
	if err := p.selection.Check(check); err != nil {
		return false, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.active != a || p.ctx.Err() != nil {
		return false, grantOperationError()
	}
	p.active = nil
	if p.automatic == nil && p.next >= len(p.names) {
		return false, nil
	}
	return true, nil
}

func pristineAttemptReservationV1(reservation *newSecretPlan, conn *Conn) bool {
	if reservation == nil || conn == nil {
		return false
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.conn == conn && reservation.creation != nil && !reservation.attempted &&
		!reservation.busy && !reservation.published && !reservation.closed && !reservation.aborting && !reservation.retired
}

func (a *HostAttemptV1) sameReservationIdentityV1(reservation *newSecretPlan) bool {
	a.request.mu.Lock()
	same := reservation != nil && a.request.newSecrets == reservation && a.request.freshChild == nil
	a.request.mu.Unlock()
	return same
}

func nativeAttemptUnreachableV1(dial *ownedNewDialV1) bool {
	if dial == nil {
		return false
	}
	// Required native-source integration: this method must identify the exact
	// final Start represented by Dial's actual result and join its SAME Wait.
	// The last captured Conn alone is insufficient: a later Start may return
	// before allocating a Conn and still leave an owned late process to reap.
	return dial.retryableUnreachableV1()
}

func narrowHostAttemptContextV1(original, caller context.Context) (context.Context, func(), error) {
	if original == nil || caller == nil {
		return nil, func() {}, grantOperationError()
	}
	if err := original.Err(); err != nil {
		return nil, func() {}, err
	}
	if err := caller.Err(); err != nil {
		return nil, func() {}, err
	}
	var call context.Context
	var cancel context.CancelFunc
	if deadline, ok := caller.Deadline(); ok {
		call, cancel = context.WithDeadline(original, deadline)
	} else {
		call, cancel = context.WithCancel(original)
	}
	stop := context.AfterFunc(caller, cancel)
	return call, func() { stop(); cancel() }, nil
}

func hostAttemptExhaustedV1() error {
	return &UserError{Msg: "every host in the original rule has been tried",
		Hint: "Check the reported host failures, then start a new request.", Code: "no_host", Kind: KindRefused}
}

func hostAttemptUncertainV1() error {
	return &UserError{Msg: "the original start has not proved that its owned resources are retired",
		Hint: "Wait for the original attempt to finish cleanup before trying another host.", Code: "host_attempt_cleanup", Kind: KindFailed}
}

// Cleanup owns a separate bounded budget, never another work or start budget.
func hostAttemptCleanupContextV1(original, caller context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(3 * sshWaitDelay)
	for _, owner := range []context.Context{original, caller} {
		if owner != nil {
			if until, ok := owner.Deadline(); ok && until.Before(deadline) {
				deadline = until
			}
		}
	}
	return context.WithDeadline(context.WithoutCancel(original), deadline)
}
