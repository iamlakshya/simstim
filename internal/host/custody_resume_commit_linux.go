//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (source *managerCustodyColdV1) CheckManifest(ctx context.Context, operation *SessionOperation, next *Manifest) error {
	if err := source.Check(ctx, operation); err != nil {
		return err
	}
	if next == nil || next.ID != operation.Primary() || next.SandboxOf != "" || next.path != "" {
		return auxiliaryUnavailableV1("the custody continuation changed its original protected selection")
	}
	if source.selection == nil {
		return checkAdmittedRestartSelectionV1(operation, &source.source, next)
	}
	if err := source.selection.checkTargetV1(operation, next); err != nil {
		return err
	}
	return ValidateManifestStart(next)
}

func (source *managerCustodyColdV1) launchFinalV1(ctx context.Context, operation *SessionOperation, m *Manifest) (*startupLaunchV1, error) {
	source.mu.Lock()
	if source.operation != operation || source.claimed || source.cancelled || source.closed || source.launch == nil || source.admission == nil || !source.admission.transferred {
		source.mu.Unlock()
		return nil, auxiliaryUnavailableV1("the custody continuation has no final original admission")
	}
	if err := source.checkLockedV1(ctx, operation); err != nil {
		source.mu.Unlock()
		return nil, err
	}
	launch := source.launch
	publication, err := beginStartupPublication(operation.ownerContext, operation)
	if err != nil {
		source.mu.Unlock()
		return nil, err
	}
	previous := launch.placement
	operation.gate.Lock()
	if !operation.active.Load() || operation.startup != nil || operation.nativeColdResume == nil || operation.nativeColdResume.custody != source {
		operation.gate.Unlock()
		source.mu.Unlock()
		_ = publication.close()
		return nil, auxiliaryUnavailableV1("the original operation already owns another native creation")
	}
	operation.startup = launch
	operation.gate.Unlock()
	launch.placement = publication
	launch.manifest = m
	source.mu.Unlock()
	if err := previous.close(); err != nil {
		return launch, err
	}
	err = publication.gateControl(ctx, func(control *placementControl) error {
		if err := checkStartingMemoryControlLockedV1(ctx, operation, m, control); err != nil {
			return err
		}
		source.mu.Lock()
		defer source.mu.Unlock()
		if err := source.checkLockedV1(ctx, operation); err != nil {
			return err
		}
		runtime, err := startupRuntimeBytes(m)
		if err != nil {
			return err
		}
		selection, err := json.Marshal(custodyFinalSelectionV1{Need: startupDigest(m.VMNeedV1), Runtime: startupDigest(runtime)})
		if err != nil {
			return err
		}
		source.claimed = true // Actual dispatch may consume the one packet.
		return launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 30, Op: "manager_custody_claim_v1", Args: selection})
	})
	if err != nil {
		return launch, err
	}
	if _, err := launch.response(ctx, 30, true); err != nil {
		return launch, err
	}
	entries, err := source.manager.dumpCustodyStoreV1(ctx, UnitName(source.operation.Primary()))
	if err != nil {
		return launch, err
	}
	if len(entries) != 0 {
		return launch, auxiliaryUnavailableV1("the claimed native manager still retains custody aliases")
	}
	// The actual new main already owns the SAME original holder OFD. Drop
	// every outside proof alias before any private values or runtime factory.
	source.mu.Lock()
	if err := source.closeProofAliasesV1(); err != nil {
		source.mu.Unlock()
		return launch, err
	}
	source.mu.Unlock()
	if err := publication.gate(ctx, func() error {
		if err := launch.check(ctx); err != nil {
			return err
		}
		if err := custodyOnlyPIDV1(source.group, launch.nativePeer().pid); err != nil {
			return err
		}
		return launch.sendCustodyReleaseV1()
	}); err != nil {
		return launch, err
	}
	if _, err := launch.response(ctx, 31, true); err != nil {
		return launch, err
	}
	err = publication.gateControl(ctx, func(control *placementControl) error {
		if err := launch.checkNativeMemoryV1(ctx, m); err != nil {
			return err
		}
		return takeoverResumePreparationNativeLockedV1(ctx, operation, operation.nativeColdResume, control)
	})
	return launch, err
}

func (source *managerCustodyColdV1) checkClaimedV1(ctx context.Context) error {
	if source == nil || !source.claimed || source.closed || source.cancelled || source.launch == nil || source.operation == nil || source.operation.startup != source.launch || source.operation.nativeColdResume == nil || source.operation.nativeColdResume.custody != source {
		return auxiliaryUnavailableV1("the custody claim has no actual original native takeover")
	}
	return errors.Join(source.launch.check(ctx), source.launch.checkNativeMemoryV1(ctx, source.launch.manifest), ctx.Err())
}

func (source *managerCustodyColdV1) checkBootstrapUnitV1(ctx context.Context, facts memoryUnitFactsV1) error {
	if source == nil || source.closed || source.cancelled || source.claimed || source.launch == nil || source.launch.nativePeer() == nil || source.group == nil || source.creation == nil || source.operation == nil || source.launch.operation != source.operation || !facts.exists || facts.maximum != source.launch.manifest.Limits.MemoryMax || facts.swap != 0 {
		return auxiliaryUnavailableV1("the native primary is not this original passive custody bootstrap")
	}
	if err := errors.Join(source.launch.check(ctx), source.launch.checkNativeMemoryV1(ctx, source.launch.manifest)); err != nil {
		return err
	}
	return custodyOnlyPIDV1(source.group, source.launch.nativePeer().pid)
}
