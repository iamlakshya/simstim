package client

import (
	"context"
	"errors"
)

// Continue resumes this exact pre-Dial paid-wake question after ConfirmGrant.
// It cannot retry a failed transport, retarget a request or replace its values.
func (a *HostAttemptV1) Continue(ctx context.Context, interactive bool) (*HostAttemptV1, error) {
	if a == nil || a.plan == nil || ctx == nil {
		return a, grantOperationError()
	}
	p := a.plan
	p.mu.Lock()
	a.mu.Lock()
	var question *scopedGrantConfirmation
	valid := p.active == a && !p.closed && p.automatic != nil && a.candidate != nil &&
		!a.closing && !a.started && !a.ready && a.dial == nil && a.running == nil &&
		a.failure != nil && a.failure.phase == hostAttemptLocalV1 &&
		errors.As(a.failure.actual, &question) && question.request == a.request &&
		autoPlacementQuestionV1(a.failure.actual)
	if !valid {
		a.mu.Unlock()
		p.mu.Unlock()
		return a, grantOperationError()
	}
	if before := grantRequestOf(ctx); before != nil && before != a.request {
		a.mu.Unlock()
		p.mu.Unlock()
		return a, grantOperationError()
	}
	a.running, a.failure = make(chan struct{}), nil
	a.mu.Unlock()
	p.mu.Unlock()
	defer a.endOperationV1()
	return a.dialTargetV1(ctx, interactive)
}

// Caller owns a.running from target creation or the exact continuation above.
func (a *HostAttemptV1) dialTargetV1(ctx context.Context, interactive bool) (*HostAttemptV1, error) {
	p := a.plan
	a.mu.Lock()
	original, name, candidate := a.ctx, a.target, a.candidate
	a.mu.Unlock()
	call, end, err := narrowHostAttemptContextV1(original, ctx)
	if err != nil {
		return a, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	defer end()
	if !wakeAllowed(ctx) {
		call = WithoutWake(call)
	}
	host, err := FindHost(name)
	if err != nil {
		return a, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	a.mu.Lock()
	a.host = cloneObservationHost(host)
	a.mu.Unlock()
	if candidate != nil {
		// This is a concrete original-source planner method. Its returned
		// context only narrows this same call to metadata-only Dial after the
		// separate guarded lifecycle wake and any exact scoped answer.
		call, err = p.automatic.authoriseWakeV1(call, candidate, p.wake)
		if err != nil {
			return a, a.recordFailureV1(err, hostAttemptLocalV1)
		}
	}
	dial, err := dialOwnedNewAttemptV1(call, host, interactive)
	a.mu.Lock()
	a.dial, a.conn = dial, dial.connection()
	conn := a.conn
	a.mu.Unlock()
	if err != nil {
		return a, a.recordFailureV1(err, hostAttemptDialV1)
	}
	if conn == nil {
		return a, a.recordFailureV1(grantOperationError(), hostAttemptLocalV1)
	}
	// The native source retains all failed subattempts as well as the winner.
	if settled, err := dial.settle(call, conn); !settled || err != nil {
		if err == nil {
			err = hostAttemptUncertainV1()
		}
		return a, a.recordFailureV1(err, hostAttemptLocalV1)
	}
	if candidate != nil {
		if err := p.refreshAutomaticAttemptV1(call, a); err != nil {
			return a, a.recordFailureV1(err, hostAttemptLocalV1)
		}
		// refresh binds this SAME R using fresh accepted facts, then stores
		// its original bound context without the temporary transition key.
	}
	a.mu.Lock()
	if a.closing || a.ctx.Err() != nil {
		a.mu.Unlock()
		return a, a.recordFailureV1(hostAttemptUncertainV1(), hostAttemptLocalV1)
	}
	a.ready = true
	a.mu.Unlock()
	return a, nil
}
