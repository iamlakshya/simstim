package client

import (
	"context"
	"errors"
	"time"
)

type savedHostRemovalAttemptV1 struct {
	owner                              *SavedHostRemovalV1
	target                             string
	post                               hostStateSource
	committed, finished, writerEntered bool
	rollbackErr                        error
	postTier                           TierEntry
	postHasTier                        bool
	writerDeadline                     time.Time
	rolledBack                         bool
}

// Prepare opens native cleanup participants under the original host operation
// and canonical membership lock. It makes no provider or remote request.
func (r *SavedHostRemovalV1) Prepare(ctx context.Context) error {
	finish, err := r.beginV1(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return withHostMembershipContext(ctx, func(locked context.Context) error { return r.prepareLockedV1(locked) })
}

func (r *SavedHostRemovalV1) newAttemptV1(ctx context.Context) error {
	if err := r.checkLockedV1(ctx, false); err != nil {
		return err
	}
	if r.attempt != nil && !r.attempt.finished {
		return savedHostRemovalErrorV1("the original native removal must finish before another attempt")
	}
	target, err := newMembershipGeneration()
	if err != nil {
		return err
	}
	if target == r.source.generation {
		return savedHostRemovalErrorV1("the removal target must differ from its original Source")
	}
	r.attempt = &savedHostRemovalAttemptV1{owner: r, target: target}
	return nil
}

func (r *SavedHostRemovalV1) prepareLockedV1(ctx context.Context) error {
	if err := r.checkLockedV1(ctx, false); err != nil {
		return err
	}
	if r.prepared {
		return nil
	}
	if len(r.participants) != 0 {
		return savedHostRemovalErrorV1("a failed native preparation still retains its original handles")
	}
	if err := r.newAttemptV1(ctx); err != nil {
		return err
	}
	for _, factory := range r.factories {
		var participant savedHostRemovalParticipantV1
		var preparation *savedHostRemovalPreparationV1
		err := r.phaseV1(ctx, r.attempt, factory.name, savedHostRemovalPreparationKindV1, nil, func(scoped context.Context, phase *savedHostRemovalPhaseV1) error {
			var err error
			preparation = &savedHostRemovalPreparationV1{phase}
			phase.preparation = preparation
			participant, err = factory.open(scoped, preparation)
			return err
		})
		if participant != nil {
			r.participants = append(r.participants, &savedHostRemovalOwnerV1{name: factory.name, participant: participant, preparation: preparation})
		}
		if err != nil {
			r.attempt.finished = true
			return err
		}
	}
	r.prepared = true
	return nil
}

// Forget stages native participants, performs the original canonical writer
// CAS and finalises only under that writer's actual absent-Source proof.
func (r *SavedHostRemovalV1) Forget(ctx context.Context) error {
	finish, err := r.beginV1(ctx)
	if err != nil {
		return err
	}
	defer finish()
	return r.forgetV1(ctx)
}

func (r *SavedHostRemovalV1) forgetV1(ctx context.Context) error {
	if r.attempt != nil && r.attempt.committed {
		return r.finaliseV1(ctx)
	}
	if err := withHostMembershipContext(ctx, func(locked context.Context) error {
		if err := r.prepareLockedV1(locked); err != nil {
			return err
		}
		if r.attempt.finished {
			return r.newAttemptV1(locked)
		}
		return nil
	}); err != nil {
		return err
	}
	attempt := r.attempt
	bound := context.WithValue(ctx, savedHostRemovalKeyV1{}, r)
	was := r.Host()
	err := UpdateHosts(bound, HostChange{Kind: "host rm", Was: &was})
	if err != nil && !attempt.committed && !attempt.finished {
		rollback, cancel := removalRollbackContextV1(ctx, attempt)
		defer cancel()
		err = errors.Join(err, withHostMembershipContext(rollback, func(locked context.Context) error { return r.rollbackLockedV1(locked) }))
	}
	if err == nil && (!attempt.committed || !attempt.finished) {
		return savedHostRemovalErrorV1("the canonical forget returned without its original native completion")
	}
	return err
}

// Retry continues only this retained original native transaction. A committed
// attempt never reconstructs a new permission from a receipt or host lookup.
func (r *SavedHostRemovalV1) Retry(ctx context.Context) error {
	finish, err := r.beginV1(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if r.attempt == nil {
		return savedHostRemovalErrorV1("the removal has no original attempt to retry")
	}
	if r.attempt.committed {
		return r.finaliseV1(ctx)
	}
	if !r.attempt.finished {
		if err := withHostMembershipContext(ctx, func(locked context.Context) error { return r.rollbackLockedV1(locked) }); err != nil {
			return err
		}
	}
	return r.forgetV1(ctx)
}

// Rollback restores only the staged original native resources while their
// original saved Source still exists. A committed forget cannot be rolled back.
func (r *SavedHostRemovalV1) Rollback(ctx context.Context) error {
	finish, err := r.beginV1(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if r.attempt == nil || r.attempt.committed {
		return savedHostRemovalErrorV1("this removal has no reversible original native stage")
	}
	return withHostMembershipContext(ctx, func(locked context.Context) error { return r.rollbackLockedV1(locked) })
}

func (r *SavedHostRemovalV1) rollbackLockedV1(ctx context.Context) error {
	if err := r.checkLockedV1(ctx, false); err != nil {
		return err
	}
	attempt := r.attempt
	attempt.rolledBack = false
	var failure error
	for index := len(r.participants) - 1; index >= 0; index-- {
		owner := r.participants[index]
		// The actual participant may carry validated precommit native custody
		// from an interrupted process. Scheduling data cannot waive its check.
		owner.staged = true
		err := r.phaseV1(ctx, attempt, owner.name, savedHostRemovalRollbackKindV1, owner.preparation, func(scoped context.Context, phase *savedHostRemovalPhaseV1) error {
			return owner.participant.rollback(scoped, &savedHostRemovalRollbackV1{phase})
		})
		if err == nil {
			owner.staged = false
		}
		failure = errors.Join(failure, err)
	}
	attempt.rollbackErr = failure
	attempt.finished = failure == nil
	attempt.rolledBack = failure == nil
	return failure
}

func (r *SavedHostRemovalV1) finaliseV1(ctx context.Context) error {
	return withHostMembershipContext(ctx, func(locked context.Context) error { return r.finaliseLockedV1(locked) })
}

func (r *SavedHostRemovalV1) finaliseLockedV1(ctx context.Context) error {
	if err := r.checkLockedV1(ctx, true); err != nil {
		return err
	}
	attempt := r.attempt
	for _, owner := range r.participants {
		if owner.finalised {
			continue
		}
		if err := r.phaseV1(ctx, attempt, owner.name, savedHostRemovalCommitKindV1, owner.preparation, func(scoped context.Context, phase *savedHostRemovalPhaseV1) error {
			return owner.participant.finalise(scoped, &savedHostRemovalCommitV1{phase})
		}); err != nil {
			return err
		}
		owner.finalised = true
	}
	attempt.finished = true
	return nil
}

func removalRollbackContextV1(ctx context.Context, attempt *savedHostRemovalAttemptV1) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(hostsWait)
	if attempt != nil && !attempt.writerDeadline.IsZero() && attempt.writerDeadline.Before(deadline) {
		deadline = attempt.writerDeadline
	}
	if original, ok := ctx.Deadline(); ok && original.Before(deadline) {
		deadline = original
	}
	return context.WithDeadline(context.WithoutCancel(ctx), deadline)
}
