package client

import (
	"context"
	"errors"
)

// A single retained worker owns a possibly blocking native/local close. A
// caller's deadline bounds its join; it does not manufacture worker completion.
type hostAttemptCleanupV1 struct {
	ctx    context.Context
	done   chan struct{}
	result hostAttemptRetirementV1
	err    error
}

type hostAttemptRetirementV1 struct {
	complete       bool
	absent         bool
	retired        bool
	reservation    *newSecretPlan
	waitError      error
	settledRefusal *settledNewRefusalV1
}

// Close stops this attempt's work and joins its actual client owners. A
// successful remote session is deliberately left running. An incomplete close
// retains the original worker, R, reservation and every owned Dial connection.
func (a *HostAttemptV1) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	original := a.ctx
	a.mu.Unlock()
	if original == nil {
		return grantOperationError()
	}
	_, err := a.joinCleanupV1(context.WithoutCancel(original))
	return err
}

func (a *HostAttemptV1) joinCleanupV1(caller context.Context) (hostAttemptRetirementV1, error) {
	a.mu.Lock()
	if a.ctx == nil || a.request == nil || a.cancel == nil || caller == nil {
		a.mu.Unlock()
		return hostAttemptRetirementV1{}, grantOperationError()
	}
	a.closing = true
	a.cancel()
	job := a.cleanup
	if job != nil {
		select {
		case <-job.done:
			if job.result.complete {
				a.mu.Unlock()
				return job.result, job.err
			}
			job = nil // the same retained owners may now be joined again
		default:
		}
	}
	if job == nil {
		budget, cancel := hostAttemptCleanupContextV1(a.ctx, caller)
		job = &hostAttemptCleanupV1{ctx: budget, done: make(chan struct{})}
		a.cleanup = job
		go func() {
			defer cancel()
			job.result, job.err = a.cleanupOwnedV1(budget)
			a.mu.Lock()
			a.closed = job.result.complete
			a.mu.Unlock()
			close(job.done)
		}()
	}
	a.mu.Unlock()
	select {
	case <-job.done:
		return job.result, job.err
	case <-caller.Done():
		return hostAttemptRetirementV1{}, errors.Join(hostAttemptUncertainV1(), caller.Err())
	case <-job.ctx.Done():
		// The channel is the join witness, even if native close races the timer.
		select {
		case <-job.done:
			return job.result, job.err
		default:
			return hostAttemptRetirementV1{}, errors.Join(hostAttemptUncertainV1(), job.ctx.Err())
		}
	}
}

func (a *HostAttemptV1) cleanupOwnedV1(ctx context.Context) (hostAttemptRetirementV1, error) {
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if running != nil {
		select {
		case <-running:
		case <-ctx.Done():
			return hostAttemptRetirementV1{}, errors.Join(hostAttemptUncertainV1(), ctx.Err())
		}
	}
	a.mu.Lock()
	request, conn, dial, started := a.request, a.conn, a.dial, a.started
	newCalled, failure, placement, priorRetirement := a.newCalled, a.failure, a.placement, a.retirement
	a.mu.Unlock()
	request.mu.Lock()
	reservation, fresh := request.newSecrets, request.freshChild
	request.mu.Unlock()
	result := hostAttemptRetirementV1{reservation: reservation}
	var abortErr error
	if !started {
		pristine := pristineAttemptReservationV1(reservation, conn)
		retired, err := request.Abort(ctx)
		abortErr = err
		// Abort(false,nil) is deliberately no proof. A previous worker may
		// carry only its own earlier positive retirement of this same plan.
		positive := retired && pristine && retiredAttemptReservationV1(request, reservation, conn)
		if positive {
			a.mu.Lock()
			a.retirement = reservation
			a.mu.Unlock()
		}
		result.retired = positive || priorRetirement != nil && priorRetirement == reservation &&
			retiredAttemptReservationV1(request, reservation, conn)
		if failure != nil && failure.settledRefusal != nil && retired && abortErr == nil {
			result.absent, result.settledRefusal = true, failure.settledRefusal
		} else if newCalled {
			result.absent = result.retired && failure != nil && failure.phase == hostAttemptNewV1 &&
				failure.reservation == reservation && failure.kind == hostAttemptNoRoomRetryV1
		} else {
			result.absent = reservation == nil && fresh == nil || result.retired
		}
	}
	var cacheErr error
	if failure != nil && failure.settledRefusal != nil && result.absent && a.plan.automatic != nil {
		a.mu.Lock()
		remembered := a.cachedRefusal == failure.settledRefusal
		a.mu.Unlock()
		if !remembered {
			cacheErr = a.plan.automatic.rememberSettledNoRoomV1(ctx, a, failure.settledRefusal)
			if cacheErr == nil {
				a.mu.Lock()
				a.cachedRefusal = failure.settledRefusal
				a.mu.Unlock()
			}
		}
	}
	// Close releases local sources and captures only; it never deletes a session.
	closeErr := request.Close()
	settled, settleErr := true, error(nil)
	if dial != nil {
		// Required source contract includes every dialStart allocation and
		// late pre-Conn Proc, each with its SAME actual single waiter.
		settled, settleErr = dial.settle(ctx, nil)
		result.waitError = dial.waitError()
	} else if conn != nil {
		settled, settleErr = false, grantOperationError()
	}
	var placementErr error
	if !started && result.absent && closeErr == nil && abortErr == nil && cacheErr == nil && settled && settleErr == nil && placement != nil {
		// Only actual pre-write retirement or a never-New owned Dial may
		// remove this exact local record. Uncertainty keeps it reserved.
		placementErr = placement.finish(ctx, "", true)
	}
	err := errors.Join(abortErr, cacheErr, closeErr, settleErr, placementErr)
	result.complete = err == nil && settled && (started || result.absent)
	if !result.complete && err == nil {
		err = hostAttemptUncertainV1()
	}
	return result, err
}

func retiredAttemptReservationV1(request *GrantRequest, reservation *newSecretPlan, conn *Conn) bool {
	if request == nil || reservation == nil || conn == nil {
		return false
	}
	request.mu.Lock()
	same := request.newSecrets == reservation && request.freshChild == nil
	request.mu.Unlock()
	if !same {
		return false
	}
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.conn == conn && reservation.creation != nil && reservation.retired &&
		reservation.aborting && !reservation.attempted && !reservation.busy && !reservation.published
}

// Close cancels work immediately and joins one retained cleanup worker. The
// selection remains owned while an attempt or the automatic planner is uncertain.
func (p *HostAttemptPlanV1) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.ctx == nil || p.cancel == nil {
		p.mu.Unlock()
		return grantOperationError()
	}
	p.closed = true
	p.cancel()
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	if p.selectionStop != nil {
		p.selectionStop()
		p.selectionStop = nil
	}
	job := p.closeJob
	if job != nil {
		select {
		case <-job.done:
			if job.result.complete {
				p.mu.Unlock()
				return job.err
			}
			job = nil
		default:
		}
	}
	if job == nil {
		budget, cancel := hostAttemptCleanupContextV1(p.ctx, p.ctx)
		job = &hostAttemptCleanupV1{ctx: budget, done: make(chan struct{})}
		p.closeJob = job
		go func() {
			defer cancel()
			job.err = p.cleanupOwnedV1(budget)
			job.result.complete = job.err == nil
			close(job.done)
		}()
	}
	p.mu.Unlock()
	select {
	case <-job.done:
		return job.err
	case <-job.ctx.Done():
		select {
		case <-job.done:
			return job.err
		default:
			return errors.Join(hostAttemptUncertainV1(), job.ctx.Err())
		}
	}
}

func (p *HostAttemptPlanV1) cleanupOwnedV1(ctx context.Context) error {
	p.mu.Lock()
	active, automatic, selection := p.active, p.automatic, p.selection
	p.mu.Unlock()
	if active != nil {
		if _, err := active.joinCleanupV1(ctx); err != nil {
			return err
		}
	}
	if automatic != nil {
		if err := automatic.Close(ctx); err != nil {
			return err
		}
	}
	return selection.Close()
}
