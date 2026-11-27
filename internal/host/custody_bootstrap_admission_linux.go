//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"time"
)

// custodyBootstrapAdmissionV1 charges the complete original cap before the
// actual StartUnit call. It has no VMM, claim or vacancy authority. The actual
// inherited-kernel source transfers this SAME leaf to final preparation.
type custodyBootstrapAdmissionV1 struct {
	source                                        *managerCustodyColdV1
	preparation                                   *bridgeResumePreparationV1
	publication                                   *runtimePublication
	value                                         ResumePreparationV1
	need, runtime                                 []byte
	selection                                     *resumePreparationSelectionV1
	published, dispatched, transferred, cancelled bool
}

func (source *managerCustodyColdV1) reserveBootstrapV1(ctx context.Context, m *Manifest) (*custodyBootstrapAdmissionV1, error) {
	p, _ := ctx.Value(bridgeResumePreparationContextKeyV1{}).(*bridgeResumePreparationV1)
	if p == nil || !p.active.Load() || p.operation != source.operation || p.bridge == nil || p.lifetime == nil || p.lifetime.Err() != nil || p.cold == nil || p.cold.custody != source || p.warm != nil || source.launch == nil || source.launch.placement == nil || m == nil || m.Limits.MemoryMax <= 0 || m.Limits.Declared != m.Limits.MemoryMax {
		return nil, resumePreparationErrorV1("custody bootstrap has no original current prepare requester and complete cap")
	}
	p.bridge.mu.Lock()
	service := p.bridge.resumePreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return nil, resumePreparationErrorV1("custody bootstrap lost its actual registered requester")
	}
	runtime, err := startupRuntimeBytes(m)
	if err != nil {
		return nil, err
	}
	selection, err := captureResumePreparationSelectionV1(ctx, source.operation, m)
	if err != nil {
		return nil, err
	}
	value := ResumePreparationV1{V: 1, Nonce: p.token, Boot: source.boot, Since: time.Now().UTC(), MemoryMax: m.Limits.Declared}
	if selection.before.MemoryMax != selection.target.MemoryMax {
		value.PreviousMemoryMax = selection.before.MemoryMax
	}
	if err := validateResumePreparationV1Data(&value); err != nil {
		return nil, err
	}
	return &custodyBootstrapAdmissionV1{source: source, selection: selection, preparation: p, publication: source.launch.placement, value: value, need: bytes.Clone(m.VMNeedV1), runtime: bytes.Clone(runtime)}, nil
}

func (admission *custodyBootstrapAdmissionV1) checkCurrentV1(ctx context.Context) error {
	if admission == nil || admission.source == nil || admission.cancelled || admission.transferred || admission.publication == nil || admission.preparation == nil || admission.preparation.operation != admission.source.operation || !admission.source.operation.Valid(admission.source.operation.Primary()) {
		return resumePreparationErrorV1("the original custody bootstrap admission ended or transferred")
	}
	if err := errors.Join(admission.source.operation.native.check(ctx), admission.source.manager.checkNativeManager(), admission.source.origin.checkIdentity()); err != nil {
		return err
	}
	current, err := admission.source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := admission.selection.checkCurrentV1(admission.source.operation, current); err != nil {
		return err
	}
	if admission.published && !reflect.DeepEqual(current.ResumePreparation, &admission.value) {
		return resumePreparationErrorV1("the original bootstrap's accounting leaf changed")
	}
	return ctx.Err()
}

func (admission *custodyBootstrapAdmissionV1) publishBeforeDispatchV1(ctx context.Context, control *placementControl) error {
	if admission == nil || admission.dispatched || admission.published || admission.cancelled || admission.transferred || control == nil {
		return resumePreparationErrorV1("the bootstrap dispatch admission was already used or ended")
	}
	if err := admission.checkCurrentV1(ctx); err != nil {
		return err
	}
	publication := admission.publication
	if actual, ok := control.Active[publication.operation.token]; !ok || actual != publication.operation.record || publication.operation.record.Kind != "resume_prepare" || publication.startup != admission.source.operation {
		return resumePreparationErrorV1("the actual bootstrap publication no longer owns its active record")
	}
	if err := checkUserDrainLockedV1(ctx, control); err != nil {
		return err
	}
	if err := errors.Join(admission.source.checkPinnedV1(ctx), admission.source.runtime.service.check(), admission.source.runtime.pin.check()); err != nil {
		return err
	}
	current, err := admission.source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if current.ResumePreparation != nil {
		return resumePreparationErrorV1("another original owner already reserved this primary")
	}
	if _, err := memoryRoomLockedV1(ctx, admission.source.operation, admission.value.MemoryMax); err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(current)
	if err != nil {
		return err
	}
	published, err := writeResumePreparationLeafLockedV1(ctx, admission.source.operation, current.VMNeedV1, runtime, nil, &admission.value)
	admission.published = published
	return err
}

func (admission *custodyBootstrapAdmissionV1) cancelV1(ctx context.Context) error {
	if admission == nil || admission.cancelled || admission.transferred {
		return nil
	}
	source := admission.source
	if source == nil || source.operation == nil || !source.operation.Valid(source.operation.Primary()) {
		return resumePreparationErrorV1("bootstrap cleanup lost its original OP and native source")
	}
	if admission.dispatched && !source.cancelled {
		return resumePreparationErrorV1("bootstrap cleanup has no positive original native settlement")
	}
	if admission.dispatched {
		if err := source.checkCancelledV1(ctx); err != nil {
			return err
		}
	}
	return withPlacementRoot(ctx, true, func(control *placementControl, _ *os.Root) error {
		if err := source.operation.native.check(ctx); err != nil {
			return err
		}
		if actual, ok := control.Active[admission.publication.operation.token]; !ok || actual != admission.publication.operation.record {
			return resumePreparationErrorV1("bootstrap cleanup lost its original active publication")
		}
		current, err := source.operation.LoadContext(ctx)
		if err != nil {
			return err
		}
		if current.ResumePreparation == nil && !admission.published {
			admission.cancelled = true
			return nil
		}
		if !reflect.DeepEqual(current.ResumePreparation, &admission.value) {
			return resumePreparationErrorV1("bootstrap cleanup cannot clear another owner's charge")
		}
		runtime, err := startupRuntimeBytes(current)
		if err != nil {
			return err
		}
		if err := admission.selection.checkCurrentV1(source.operation, current); err != nil {
			return err
		}
		_, err = writeResumePreparationLeafLockedV1(ctx, source.operation, current.VMNeedV1, runtime, &admission.value, nil)
		if err == nil {
			admission.cancelled = true
		}
		return err
	}, false)
}

func (source *managerCustodyColdV1) adoptBootstrapPreparationV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, preview *Manifest, nonce string) (*resumePreparationLeaseV1, error) {
	admission := source.admission
	if admission == nil || !admission.published || !admission.dispatched || admission.cancelled || admission.transferred || preview == nil || preview.Limits.MemoryMax != admission.value.MemoryMax || preview.Limits.Declared != admission.value.MemoryMax || nonce != admission.value.Nonce {
		return nil, resumePreparationErrorV1("the final preparation cannot replace or enlarge its original bootstrap charge")
	}
	origin, err := requireResumePreparationAccountingOriginV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := admission.checkCurrentV1(ctx); err != nil {
		return nil, err
	}
	if err := source.Check(ctx, operation); err != nil {
		return nil, err
	}
	lease := &resumePreparationLeaseV1{selection: admission.selection, origin: origin, operation: operation, cold: cold, publication: origin.preparation.publication, value: admission.value, need: bytes.Clone(admission.need), runtime: bytes.Clone(admission.runtime), published: true}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.resumePreparation != nil || operation.startup != nil || cold.custody != source {
		return nil, resumePreparationErrorV1("the original operation cannot transfer another bootstrap admission")
	}
	operation.resumePreparation = lease
	admission.transferred = true
	return lease, nil
}
