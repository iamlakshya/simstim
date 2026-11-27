package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
)

type resumePreparationAccountingOriginV1 struct {
	preparation *bridgeResumePreparationV1
	rewind      *bridgeRewindPreparationV1
}

func requireResumePreparationAccountingOriginV1(ctx context.Context, operation *SessionOperation) (*resumePreparationAccountingOriginV1, error) {
	if ctx == nil {
		return nil, resumePreparationErrorV1("the memory reservation has no original caller")
	}
	p, _ := ctx.Value(bridgeResumePreparationContextKeyV1{}).(*bridgeResumePreparationV1)
	if p == nil {
		rewind, _ := ctx.Value(bridgeRewindPreparationContextKeyV1{}).(*bridgeRewindPreparationV1)
		origin := &resumePreparationAccountingOriginV1{rewind: rewind}
		if rewind == nil || rewind.operation != operation || rewind.cold == nil {
			return nil, resumePreparationErrorV1("the memory reservation has no original cold producer")
		}
		if err := origin.check(ctx, operation, rewind.cold); err != nil {
			return nil, err
		}
		return origin, nil
	}
	origin := &resumePreparationAccountingOriginV1{preparation: p}
	if p == nil || p.operation != operation || p.cold == nil {
		return nil, resumePreparationErrorV1("the memory reservation has no original cold producer")
	}
	if err := origin.check(ctx, operation, p.cold); err != nil {
		return nil, err
	}
	return origin, nil
}

// The accounting kernel holds the placement control already. This check
// consumes the SAME actual producer and descriptors without reacquiring it.
// Cleanup may use an independent context after original worker cancellation.
func (origin *resumePreparationAccountingOriginV1) check(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1) error {
	if origin == nil || ctx == nil || operation == nil || cold == nil {
		return resumePreparationErrorV1("the original accounting source is absent")
	}
	if origin.rewind != nil {
		if origin.preparation != nil {
			return resumePreparationErrorV1("the native accounting origin has two producers")
		}
		return origin.rewind.checkAccountingOriginV1(ctx, operation, cold)
	}
	p := origin.preparation
	if p == nil || !p.active.Load() || p.bridge == nil || p.operation != operation || p.cold != cold || p.warm != nil || p.publication == nil || p.publication.startup != operation || p.publication.operation == nil || len(p.token) != 64 || !operation.Valid(operation.Primary()) {
		return resumePreparationErrorV1("the accounting source differs from the original cold preparation")
	}
	p.bridge.mu.Lock()
	service := p.bridge.resumePreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return resumePreparationErrorV1("the original accounting producer is no longer registered")
	}
	return cold.Check(ctx, operation)
}

func (origin *resumePreparationAccountingOriginV1) publicationV1() *runtimePublication {
	if origin == nil {
		return nil
	}
	if origin.rewind != nil {
		return origin.rewind.publication
	}
	if origin.preparation != nil {
		return origin.preparation.publication
	}
	return nil
}
func (origin *resumePreparationAccountingOriginV1) lifetimeV1() context.Context {
	if origin == nil {
		return nil
	}
	if origin.rewind != nil {
		return origin.rewind.execution
	}
	if origin.preparation != nil {
		return origin.preparation.lifetime
	}
	return nil
}
func (origin *resumePreparationAccountingOriginV1) nonceV1() string {
	if origin == nil {
		return ""
	}
	if origin.rewind != nil {
		return origin.rewind.token
	}
	if origin.preparation != nil {
		return origin.preparation.token
	}
	return ""
}
func (origin *resumePreparationAccountingOriginV1) kindV1() string {
	if origin != nil && origin.rewind != nil {
		return "rewind_prepare"
	}
	return "resume_prepare"
}

func (origin *resumePreparationAccountingOriginV1) committedLeaseV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, args api.ResumeArgs) (*resumePreparationLeaseV1, error) {
	if origin == nil {
		return nil, resumePreparationErrorV1("the cold restart has no actual committed producer")
	}
	if p := origin.rewind; p != nil {
		if origin.preparation != nil || p.phase.Load() != rewindRestartingV1 || !p.artifactsRefreshed || p.accounting == nil {
			return nil, rewindFailureV1("the actual rewind disk commit has no complete restart owner")
		}
		if err := p.checkCommittedV1(ctx, operation, cold, args); err != nil {
			return nil, err
		}
		return p.accounting, nil
	}
	p := origin.preparation
	if p == nil || p.cold != cold || !args.Prepared.Valid() || args.Prepared.Token != p.token || !p.consumed || p.accounting == nil {
		return nil, resumePreparationErrorV1("the cold request lost its original preparation")
	}
	return p.accounting, nil
}
