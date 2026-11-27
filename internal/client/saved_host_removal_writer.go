package client

import (
	"context"
	"errors"
	"slices"
)

// The joined-config owner calls this from its canonical forget guard. A
// decoded ownership record or an unprepared generic context is insufficient.
func requireSavedHostRemovalParticipantV1(ctx context.Context, original Host, consumer string) error {
	r, _ := ctx.Value(savedHostRemovalKeyV1{}).(*SavedHostRemovalV1)
	if r == nil || !SameEntry(r.host, original) || !r.prepared || r.attempt == nil || !r.attempt.writerEntered || r.attempt.committed || r.attempt.finished {
		return savedHostRemovalErrorV1("the saved host has no prepared original native removal participant")
	}
	r.mu.Lock()
	active := r.busy && !r.closed
	r.mu.Unlock()
	if !active {
		return savedHostRemovalErrorV1("the original native removal is not active")
	}
	for _, owner := range r.participants {
		if owner.name == consumer && owner.participant != nil && owner.preparation != nil && owner.preparation.operation == r {
			return r.checkLockedV1(ctx, false)
		}
	}
	return savedHostRemovalErrorV1("the original native removal participant was not prepared")
}

// Called immediately after ApplyHostChange, before pin/tier side effects.
func checkSavedHostRemovalChangeV1(ctx context.Context, change HostChange, _ []Host) error {
	r, _ := ctx.Value(savedHostRemovalKeyV1{}).(*SavedHostRemovalV1)
	if r == nil {
		return nil
	}
	if err := requireSavedHostRemovalWriterV1(ctx, r, change, false); err != nil {
		return err
	}
	if err := r.checkLockedV1(ctx, false); err != nil {
		return err
	}
	r.attempt.writerEntered = true
	if deadline, ok := ctx.Deadline(); ok {
		r.attempt.writerDeadline = deadline
	}
	return nil
}

func requireSavedHostRemovalWriterV1(ctx context.Context, r *SavedHostRemovalV1, change HostChange, entered bool) error {
	if r == nil || ctx.Value(savedHostRemovalKeyV1{}) != r || change.Now != nil || change.Was == nil || !SameEntry(*change.Was, r.host) {
		return savedHostRemovalErrorV1("the canonical forget differs from its original removal")
	}
	r.mu.Lock()
	valid := r.busy && !r.closed
	r.mu.Unlock()
	if !valid || r.attempt == nil || r.attempt.owner != r || r.attempt.committed || r.attempt.finished || r.attempt.writerEntered != entered {
		return savedHostRemovalErrorV1("the canonical forget has no matching active original attempt")
	}
	return r.checkOperationV1(ctx)
}

// Called after the existing canonical hooks are built. Ordinary non-transaction
// writers keep their existing behaviour; managed participants require this
// actual producer through their own canonical introduction/forget guard.
func attachSavedHostRemovalHooksV1(ctx context.Context, change HostChange, hooks hostFileHooks) (hostFileHooks, error) {
	r, _ := ctx.Value(savedHostRemovalKeyV1{}).(*SavedHostRemovalV1)
	if r == nil {
		return hooks, nil
	}
	if err := requireSavedHostRemovalWriterV1(ctx, r, change, true); err != nil {
		return hooks, err
	}
	attempt := r.attempt
	if hooks.membershipGeneration != "" || hooks.membershipSourceGeneration != "" {
		return hooks, savedHostRemovalErrorV1("another removal purpose already selected this writer's generation")
	}
	hooks.membershipGeneration, hooks.membershipSourceGeneration = attempt.target, r.source.generation
	priorBefore := hooks.beforeSave
	hooks.beforeSave = func() error {
		if err := r.stageLockedV1(ctx, attempt); err != nil {
			return err
		}
		if priorBefore != nil {
			if err := priorBefore(); err != nil {
				return err
			}
		}
		return r.checkLockedV1(ctx, false)
	}
	priorRollback := hooks.rollback
	hooks.rollback = func() {
		if priorRollback != nil {
			priorRollback()
		}
		rollback, cancel := removalRollbackContextV1(ctx, attempt)
		defer cancel()
		attempt.rollbackErr = r.rollbackLockedV1(rollback)
	}
	priorFailure := hooks.failure
	hooks.failure = func(cause error) error {
		if priorFailure != nil {
			cause = priorFailure(cause)
		}
		return errors.Join(cause, attempt.rollbackErr)
	}
	priorObserve := hooks.observeCommit
	hooks.observeCommit = func() error {
		if err := r.recordCommittedV1(ctx, change, attempt); err != nil {
			return err
		}
		if priorObserve != nil {
			if err := priorObserve(); err != nil {
				return err
			}
		}
		return r.finaliseLockedV1(ctx)
	}
	return hooks, nil
}

func (r *SavedHostRemovalV1) stageLockedV1(ctx context.Context, attempt *savedHostRemovalAttemptV1) error {
	if attempt == nil || attempt != r.attempt || !attempt.writerEntered {
		return savedHostRemovalErrorV1("the native stage has no original canonical writer")
	}
	if err := r.checkLockedV1(ctx, false); err != nil {
		return err
	}
	for _, owner := range r.participants {
		owner.staged = true
		if err := r.phaseV1(ctx, attempt, owner.name, savedHostRemovalStageKindV1, owner.preparation, func(scoped context.Context, phase *savedHostRemovalPhaseV1) error {
			return owner.participant.stage(scoped, &savedHostRemovalStageV1{phase})
		}); err != nil {
			return err
		}
	}
	return nil
}

func (r *SavedHostRemovalV1) recordCommittedV1(ctx context.Context, change HostChange, attempt *savedHostRemovalAttemptV1) error {
	if attempt == nil || attempt != r.attempt || !attempt.writerEntered || attempt.committed || change.Now != nil || change.Was == nil || !SameEntry(*change.Was, r.host) {
		return savedHostRemovalErrorV1("the committed removal lost its original writer")
	}
	if err := requireHostMembershipContext(ctx); err != nil {
		return err
	}
	if err := r.checkOperationV1(ctx); err != nil {
		return err
	}
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(hosts, func(h Host) bool { return h.Name == r.host.Name }) {
		return savedHostRemovalErrorV1("the canonical writer did not remove the original saved name")
	}
	post, err := captureHostStateSource(ctx, hosts, false)
	if err != nil {
		return err
	}
	if !post.exists || post.generation != attempt.target || post.path != r.source.path {
		return savedHostRemovalErrorV1("the committed forget did not publish its selected original target")
	}
	// Capture is from this exact writer callback while membership and original
	// OP are held. No later receipt/absence lookup can recreate this handoff.
	tiers, err := readTiersFile()
	if err != nil {
		return err
	}
	entry, exists := tiers.Entry(r.host.Name)
	if exists && (!r.hasTier || !sameRemovalTierV1(entry, r.tier)) {
		return savedHostRemovalErrorV1("the committed writer observed a different original host identity")
	}
	attempt.postTier, attempt.postHasTier = entry, exists
	attempt.post, attempt.committed = post, true
	return r.checkLockedV1(ctx, true)
}
