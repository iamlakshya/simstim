//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"reflect"
)

func (source *managerCustodyColdV1) checkDormantV1(ctx context.Context) error {
	if source == nil || source.custodyKernelSourceV1 == nil || source.operation == nil || source.launch != nil || source.closed || source.cancelled || source.claimed || !source.operation.Valid(source.operation.Primary()) || source.boot != BootID() {
		return auxiliaryUnavailableV1("the captured dormant custody source ended or activated")
	}
	if err := errors.Join(source.origin.checkIdentity(), source.operation.native.check(ctx), source.checkPinnedV1(ctx), source.runtime.service.check(), source.runtime.pin.check()); err != nil {
		return err
	}
	current, err := source.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if current.Limits != source.source.Limits || !bytes.Equal(current.VMNeedV1, source.source.VMNeedV1) || !reflect.DeepEqual(current.RuntimeManifest, source.source.RuntimeManifest) {
		return auxiliaryUnavailableV1("the dormant custody source changed its original protected selection")
	}
	return ctx.Err()
}

// The actual registered bridge requester calls this only after authenticating
// its fresh target. Capturing a dormant source cannot dispatch StartUnit.
func activateManagerCustodyPreparationV1(ctx context.Context, operation *SessionOperation, target *Manifest) error {
	p, _ := ctx.Value(bridgeResumePreparationContextKeyV1{}).(*bridgeResumePreparationV1)
	if p == nil || p.operation != operation || p.cold == nil || !p.active.Load() || p.lifetime == nil || p.lifetime.Err() != nil {
		return resumePreparationErrorV1("custody activation has no original admitted prepare requester")
	}
	if p.cold.custody == nil {
		return p.cold.CheckManifest(ctx, operation, target)
	}
	source := p.cold.custody
	source.mu.Lock()
	defer source.mu.Unlock()
	if err := source.checkDormantV1(ctx); err != nil {
		return err
	}
	selection, err := captureResumePreparationSelectionV1(ctx, operation, target)
	if err != nil {
		return err
	}
	source.selection = selection
	if err := source.checkTargetResidualV1(ctx, target); err != nil {
		return err
	}
	argv := []string{source.origin.executable, "supervise", operation.Primary()}
	if err := source.manager.rewriteCustodyRuntimeV1(ctx, source.runtime, target, source.origin.executable, argv); err != nil {
		return err
	}
	if err := source.startBootstrapV1(ctx, target, argv); err != nil {
		return err
	}
	if err := source.captureRolesV1(ctx); err != nil {
		return err
	}
	return source.checkLockedV1(ctx, operation)
}
