package client

import (
	"context"
	"sync"
	"sync/atomic"
)

type savedHostRemovalParticipantV1 interface {
	stage(context.Context, *savedHostRemovalStageV1) error
	rollback(context.Context, *savedHostRemovalRollbackV1) error
	finalise(context.Context, *savedHostRemovalCommitV1) error
	close() error
}

type savedHostRemovalFactoryV1 struct {
	name string
	open func(context.Context, *savedHostRemovalPreparationV1) (savedHostRemovalParticipantV1, error)
}

type savedHostRemovalOwnerV1 struct {
	name              string
	participant       savedHostRemovalParticipantV1
	staged, finalised bool
	preparation       *savedHostRemovalPreparationV1
}

var savedHostRemovalRegistryV1 = struct {
	sync.Mutex
	frozen    bool
	factories []savedHostRemovalFactoryV1
}{}

func registerSavedHostRemovalParticipantV1(name string, open func(context.Context, *savedHostRemovalPreparationV1) (savedHostRemovalParticipantV1, error)) error {
	savedHostRemovalRegistryV1.Lock()
	defer savedHostRemovalRegistryV1.Unlock()
	if savedHostRemovalRegistryV1.frozen || name == "" || open == nil {
		return savedHostRemovalErrorV1("the removal participant registration is missing or already frozen")
	}
	for _, registered := range savedHostRemovalRegistryV1.factories {
		if registered.name == name {
			return savedHostRemovalErrorV1("a removal participant already uses that name")
		}
	}
	savedHostRemovalRegistryV1.factories = append(savedHostRemovalRegistryV1.factories, savedHostRemovalFactoryV1{name: name, open: open})
	return nil
}

func savedHostRemovalFactoriesV1() []savedHostRemovalFactoryV1 {
	savedHostRemovalRegistryV1.Lock()
	defer savedHostRemovalRegistryV1.Unlock()
	savedHostRemovalRegistryV1.frozen = true
	return append([]savedHostRemovalFactoryV1(nil), savedHostRemovalRegistryV1.factories...)
}

type savedHostRemovalPhaseKeyV1 struct{}

type savedHostRemovalPhaseKindV1 uint8

const (
	savedHostRemovalPreparationKindV1 savedHostRemovalPhaseKindV1 = iota + 1
	savedHostRemovalStageKindV1
	savedHostRemovalRollbackKindV1
	savedHostRemovalCommitKindV1
)

type savedHostRemovalPhaseV1 struct {
	operation   *SavedHostRemovalV1
	attempt     *savedHostRemovalAttemptV1
	consumer    string
	ctx         context.Context
	kind        savedHostRemovalPhaseKindV1
	active      atomic.Bool
	preparation *savedHostRemovalPreparationV1
}

// Phase types are distinct narrow native scopes. They are never serialised,
// and no field returned by their reporting methods can recreate a scope.
type savedHostRemovalPreparationV1 struct{ *savedHostRemovalPhaseV1 }
type savedHostRemovalStageV1 struct{ *savedHostRemovalPhaseV1 }
type savedHostRemovalRollbackV1 struct{ *savedHostRemovalPhaseV1 }
type savedHostRemovalCommitV1 struct{ *savedHostRemovalPhaseV1 }

func (p *savedHostRemovalPhaseV1) check(ctx context.Context) error {
	if p == nil || ctx == nil || !p.active.Load() || ctx.Value(savedHostRemovalPhaseKeyV1{}) != p || p.operation == nil || p.attempt == nil || p.operation.attempt != p.attempt || grantRequestOf(ctx) != nil {
		return savedHostRemovalErrorV1("the original native removal phase is no longer active")
	}
	if err := p.operation.checkOperationV1(ctx); err != nil {
		return err
	}
	return p.operation.checkLockedV1(ctx, p.kind == savedHostRemovalCommitKindV1)
}

// Host is reporting data and cannot recreate a removal scope.
func (p *savedHostRemovalPhaseV1) Host() Host {
	if p == nil || p.operation == nil {
		return Host{}
	}
	return p.operation.Host()
}

// HostID reports the original identity without granting an operation.
func (p *savedHostRemovalPhaseV1) HostID() string {
	if p == nil || p.operation == nil {
		return ""
	}
	return p.operation.HostID()
}

// Context belongs only to this active callback and is revoked on return.
func (p *savedHostRemovalPhaseV1) Context() context.Context {
	if p == nil {
		return nil
	}
	return p.ctx
}

// TargetGeneration reports this actual writer's selected target as data.
func (p *savedHostRemovalPhaseV1) TargetGeneration() string {
	if p == nil || p.attempt == nil {
		return ""
	}
	return p.attempt.target
}

// Consumer names the registered native owner; the name grants nothing alone.
func (p *savedHostRemovalPhaseV1) Consumer() string {
	if p == nil {
		return ""
	}
	return p.consumer
}

func (p *savedHostRemovalPhaseV1) checkParticipant(ctx context.Context, original *savedHostRemovalPreparationV1) error {
	if err := p.check(ctx); err != nil {
		return err
	}
	if original == nil || original.savedHostRemovalPhaseV1 == nil || original.kind != savedHostRemovalPreparationKindV1 || p.preparation != original || original.operation != p.operation || original.consumer != p.consumer {
		return savedHostRemovalErrorV1("the native participant belongs to another original removal producer")
	}
	return nil
}

func (r *SavedHostRemovalV1) phaseV1(ctx context.Context, attempt *savedHostRemovalAttemptV1, name string, kind savedHostRemovalPhaseKindV1, preparation *savedHostRemovalPreparationV1, run func(context.Context, *savedHostRemovalPhaseV1) error) error {
	if kind < savedHostRemovalPreparationKindV1 || kind > savedHostRemovalCommitKindV1 {
		return savedHostRemovalErrorV1("the native removal phase is unknown")
	}
	scoped, cancel := context.WithCancel(ctx)
	defer cancel()
	phase := &savedHostRemovalPhaseV1{operation: r, attempt: attempt, consumer: name, kind: kind, preparation: preparation}
	phase.ctx = context.WithValue(scoped, savedHostRemovalPhaseKeyV1{}, phase)
	phase.active.Store(true)
	defer phase.active.Store(false)
	if err := phase.check(phase.ctx); err != nil {
		return err
	}
	return run(phase.ctx, phase)
}
