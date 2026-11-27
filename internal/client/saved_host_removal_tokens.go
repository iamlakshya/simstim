package client

import "context"

func checkSavedHostRemovalPhaseV1(ctx context.Context, phase *savedHostRemovalPhaseV1, kind savedHostRemovalPhaseKindV1) error {
	if phase == nil || phase.kind != kind {
		return savedHostRemovalErrorV1("the native removal token belongs to another phase")
	}
	return phase.check(ctx)
}

// Check validates this original constructor's read-only preparation scope.
func (p *savedHostRemovalPreparationV1) Check(ctx context.Context) error {
	if p == nil {
		return savedHostRemovalErrorV1("the native preparation token is missing")
	}
	return checkSavedHostRemovalPhaseV1(ctx, p.savedHostRemovalPhaseV1, savedHostRemovalPreparationKindV1)
}

// CheckParticipant also binds the exact original preparation object.
func (p *savedHostRemovalPreparationV1) CheckParticipant(ctx context.Context, original *savedHostRemovalPreparationV1) error {
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.checkParticipant(ctx, original)
}

// Check validates only this producer's active precommit stage.
func (p *savedHostRemovalStageV1) Check(ctx context.Context) error {
	if p == nil {
		return savedHostRemovalErrorV1("the native stage token is missing")
	}
	return checkSavedHostRemovalPhaseV1(ctx, p.savedHostRemovalPhaseV1, savedHostRemovalStageKindV1)
}

// CheckParticipant prevents a stage from using another constructor's custody.
func (p *savedHostRemovalStageV1) CheckParticipant(ctx context.Context, original *savedHostRemovalPreparationV1) error {
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.checkParticipant(ctx, original)
}

// Check validates only this producer's active original-Source rollback.
func (p *savedHostRemovalRollbackV1) Check(ctx context.Context) error {
	if p == nil {
		return savedHostRemovalErrorV1("the native rollback token is missing")
	}
	return checkSavedHostRemovalPhaseV1(ctx, p.savedHostRemovalPhaseV1, savedHostRemovalRollbackKindV1)
}

// CheckParticipant binds rollback to the same retained native participant.
func (p *savedHostRemovalRollbackV1) CheckParticipant(ctx context.Context, original *savedHostRemovalPreparationV1) error {
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.checkParticipant(ctx, original)
}

// Check requires the actual writer's committed absent Source and original OP.
func (p *savedHostRemovalCommitV1) Check(ctx context.Context) error {
	if p == nil {
		return savedHostRemovalErrorV1("the native commit token is missing")
	}
	return checkSavedHostRemovalPhaseV1(ctx, p.savedHostRemovalPhaseV1, savedHostRemovalCommitKindV1)
}

// CheckParticipant permits finalisation only for the original native owner.
func (p *savedHostRemovalCommitV1) CheckParticipant(ctx context.Context, original *savedHostRemovalPreparationV1) error {
	if err := p.Check(ctx); err != nil {
		return err
	}
	return p.checkParticipant(ctx, original)
}
