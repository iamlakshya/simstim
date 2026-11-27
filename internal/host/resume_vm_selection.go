package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/vmneed"
)

// Only the actual second accepted bridge request binds these original runtime
// and agents producers to the retained native Resume. Wire data cannot mint it.
type resumeVMSelectionV1 struct {
	preparation *bridgeResumePreparationV1
	before      Manifest
	target      Manifest
	raw         []byte
	producers   *newRequestWitnessV1 // shared producer validation only; never a New grant
}

func (p *bridgeResumePreparationV1) bindVMSelectionV1(ctx context.Context, raw json.RawMessage, a api.ResumeArgs, origin json.RawMessage) (*resumeVMSelectionV1, error) {
	if p == nil || p.bridge == nil || p.cold == nil || p.warm != nil || p.accounting != nil || p.vm != nil || !p.pendingVM || p.operation == nil || len(a.VMNeedV1) == 0 {
		return nil, resumePreparationErrorV1("the cold VM preparation has no original selection continuation")
	}
	if err := p.cold.Check(ctx, p.operation); err != nil {
		return nil, err
	}
	before, err := p.operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	old, err := vmneed.DecodeV1(before.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	fresh, err := vmneed.DecodeV1(a.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if old.Driver != fresh.Driver || old.CPUs != fresh.CPUs || old.DiskMax != fresh.DiskMax || !reflect.DeepEqual(old.Image, fresh.Image) {
		return nil, vmNeedError("resume sizing changed the original driver, image, CPU or disk selection")
	}
	expected, err := vmneed.EncodeImageV1(fresh.Image)
	if err != nil || len(a.ExpectedImageV1) == 0 || !bytes.Equal(expected, a.ExpectedImageV1) {
		return nil, resumeImageMismatchV1()
	}
	selectedRuntime, runtimeUse, err := p.bridge.runtime.selectedVMRuntimeOriginV1(ctx, fresh.Driver, fresh.Runtime, origin)
	if err != nil {
		return nil, err
	}
	selectedAgents, err := p.bridge.agents.selected(fresh.Agents)
	if err != nil {
		return nil, err
	}
	selected, err := selectedRuntimeInventoryV1(ctx, selectedRuntime, runtimeUse)
	if err != nil || !reflect.DeepEqual(selected, fresh.Runtime) {
		return nil, errors.Join(newRuntimeError("the resume lost its original signed runtime producer"), err)
	}
	disk, err := selectedAgents.Inventory()
	if err != nil || disk.Content != fresh.Agents {
		return nil, errors.Join(newRuntimeError("the resume lost its original installed agents producer"), err)
	}
	budget, err := vmneed.BudgetFor(fresh.GuestRAM)
	if err != nil {
		return nil, err
	}
	target := CloneManifest(*before)
	target.VMNeedV1 = bytes.Clone(a.VMNeedV1)
	target.RuntimeManifest = &selected
	target.Agents = fresh.Agents.SHA256
	target.Limits.MemoryMax, target.Limits.Declared = budget.Limit, budget.Limit
	if err := fresh.ValidateWall(runtime.GOARCH, target.Wall.Level, target.Wall.Driver, budget.Limit); err != nil {
		return nil, err
	}
	return &resumeVMSelectionV1{preparation: p, before: CloneManifest(*before), target: target, raw: bytes.Clone(raw), producers: &newRequestWitnessV1{bridge: p.bridge, runtime: selectedRuntime, runtimeUse: runtimeUse, agents: selectedAgents}}, nil
}

func resumeVMSelectionForArgsV1(ctx context.Context, operation *SessionOperation, a api.ResumeArgs) (*resumeVMSelectionV1, error) {
	if ctx == nil {
		return nil, newRuntimeError("the fresh resume Need has no original request lifetime")
	}
	if len(a.VMNeedV1) == 0 {
		return nil, nil
	}
	p, _ := ctx.Value(bridgeResumePreparationContextKeyV1{}).(*bridgeResumePreparationV1)
	if p == nil || p.operation != operation || p.vm == nil || p.vm.preparation != p || !bytes.Equal(p.vm.target.VMNeedV1, a.VMNeedV1) || !p.active.Load() || p.lifetime.Err() != nil {
		return nil, newRuntimeError("the fresh resume Need has no original accepted cold producer")
	}
	return p.vm, nil
}

func sameRestartExecutionSelectionV1(a, b *Manifest) bool {
	return a != nil && b != nil && reflect.DeepEqual(a.Limits, b.Limits) && bytes.Equal(a.VMNeedV1, b.VMNeedV1) && reflect.DeepEqual(a.RuntimeManifest, b.RuntimeManifest) && a.Agents == b.Agents
}

func (s *resumeVMSelectionV1) checkLockedV1(ctx context.Context, control *placementControl) error {
	if s == nil || s.preparation == nil || s.preparation.vm != s || s.preparation.bridge == nil || !s.preparation.active.Load() || s.preparation.lifetime.Err() != nil || s.producers == nil {
		return newRuntimeError("the original cold resume VM selection ended")
	}
	return s.producers.checkProducersLocked(ctx, control)
}

func lockRestartVMProducersV1(operation *SessionOperation) (func(), error) {
	operation.gate.Lock()
	receipt := operation.restartMemory
	operation.gate.Unlock()
	if receipt == nil || receipt.vm == nil {
		return func() {}, nil
	}
	return receipt.vm.lockProducersV1()
}
func (selection *resumeVMSelectionV1) lockProducersV1() (func(), error) {
	if selection == nil || selection.producers == nil || selection.producers.runtime == nil || selection.producers.agents == nil {
		return nil, newRuntimeError("the restart lost its original VM producers")
	}
	selection.producers.runtime.mu.RLock()
	selection.producers.agents.mu.RLock()
	return func() { selection.producers.agents.mu.RUnlock(); selection.producers.runtime.mu.RUnlock() }, nil
}

func (s *resumeVMSelectionV1) verifyBeforePlacementV1(ctx context.Context) error {
	if s == nil || s.producers == nil || s.producers.runtime == nil {
		return newRuntimeError("the restart has no original full runtime producer")
	}
	if s.producers.runtimeUse != nil {
		return s.producers.runtimeUse.verifyLocked(ctx)
	}
	return s.producers.runtime.checkLocked(ctx)
}
func (s *resumeVMSelectionV1) checkTargetLockedV1(ctx context.Context, control *placementControl, target *Manifest) error {
	if target == nil || s == nil || s.producers == nil {
		return newRuntimeError("the restart writer lost its original VM target")
	}
	if err := s.checkLockedV1(ctx, control); err != nil {
		return err
	}
	return checkSelectedRuntimeActiveLockedV1(ctx, s.producers.runtime, s.producers.runtimeUse, target.RuntimeManifest, wallDriver(target))
}
