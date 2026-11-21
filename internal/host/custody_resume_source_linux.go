//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"
)

// managerCustodyColdV1 is a distinct closed source. It retains the SAME
// manager, new bootstrap invocation and five actual inherited kernel aliases.
// No ordinary vacancy, StoppedSession or changed-boot source is constructed.
type managerCustodyColdV1 struct {
	mu sync.Mutex
	*custodyKernelSourceV1
	admission *custodyBootstrapAdmissionV1
	selection *resumePreparationSelectionV1
}

func captureManagerCustodyResumeV1(ctx context.Context, operation *SessionOperation) (result *managerCustodyColdV1, failure error) {
	if operation == nil || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, auxiliaryUnavailableV1("manager custody has no original outside operation")
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !isWalledMemoryV1(m) || m.SandboxOf != "" || m.Auxiliary != nil || m.Run.BootID != BootID() {
		return nil, auxiliaryUnavailableV1("the selected session has no supported same-boot custody source")
	}
	kernel, err := captureCustodyKernelSourceV1(ctx, operation, m)
	if err != nil {
		if kernel != nil {
			return &managerCustodyColdV1{custodyKernelSourceV1: kernel}, err
		}
		return nil, err
	}
	source := &managerCustodyColdV1{custodyKernelSourceV1: kernel}
	complete := false
	defer func() {
		if !complete {
			cleanup := source.Close()
			failure = errors.Join(failure, cleanup)
			if cleanup != nil {
				result = source
			}
		}
	}()
	// Capture does not start a helper. The actual requester must first supply
	// and admit its current target selection, including a fresh client VM Need.
	if err := source.checkDormantV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return source, nil
}

func (source *custodyKernelSourceV1) checkPinnedV1(ctx context.Context) error {
	if err := errors.Join(ctx.Err(), source.manager.checkNativeManager()); err != nil {
		return err
	}
	for _, item := range []struct{ iface, name, want string }{{systemdUnitInterface, "Id", UnitName(source.operation.Primary())}, {systemdUnitInterface, "LoadState", "loaded"}, {systemdUnitInterface, "ActiveState", "inactive"}, {systemdUnitInterface, "SubState", "dead-resources-pinned"}} {
		value, err := source.manager.getProperty(ctx, source.path, item.iface, item.name)
		if err != nil {
			return err
		}
		if got, ok := value.Value().(string); !ok || got != item.want {
			return sessionLeaseError("the same-boot service has no exact retained manager custody state")
		}
	}
	value, err := source.manager.getProperty(ctx, source.path, systemdServiceInterface, "NFileDescriptorStore")
	if err != nil {
		return err
	}
	if count, ok := value.Value().(uint32); !ok || count != custodyBundleSizeV1 {
		return sessionLeaseError("the retained manager has no complete native custody bundle")
	}
	if enabled, err := source.manager.custodyPropertiesV1(ctx, source.path); err != nil || !enabled {
		return errors.Join(sessionLeaseError("the retained service has no closed custody properties"), err)
	}
	aux, err := AuxUnitNameV1(source.operation.Primary())
	if err != nil {
		return err
	}
	if err := source.manager.requireAuxiliaryUnitAbsentV1(ctx, aux); err != nil {
		return err
	}
	facts, err := readAuxMemoryUnitV1(ctx, source.operation.Primary())
	if err != nil || facts.exists {
		return errors.Join(sessionLeaseError("a retained session still has an auxiliary native lifetime"), err)
	}
	return source.manager.checkCustodyPinV1(ctx, UnitName(source.operation.Primary()))
}

func (source *managerCustodyColdV1) Check(ctx context.Context, operation *SessionOperation) error {
	if source == nil || source.custodyKernelSourceV1 == nil {
		return auxiliaryUnavailableV1("the actual retained manager source is absent")
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.checkLockedV1(ctx, operation)
}

func (source *managerCustodyColdV1) checkLockedV1(ctx context.Context, operation *SessionOperation) error {
	if source.closed || source.operation != operation || operation == nil || !operation.Valid(operation.Primary()) || source.boot != BootID() {
		return auxiliaryUnavailableV1("the actual inherited manager source ended or changed")
	}
	if source.cancelled {
		return source.checkCancelledV1(ctx)
	}
	if source.launch == nil {
		return source.checkDormantV1(ctx)
	}
	if source.claimed {
		return source.checkClaimedV1(ctx)
	}
	if len(source.roles) != custodyBundleSizeV1 {
		return auxiliaryUnavailableV1("the original custody proof has no complete inherited kernel bundle")
	}
	if err := errors.Join(source.origin.checkIdentity(), operation.native.check(ctx), source.launch.check(ctx), source.runtime.service.check(), source.runtime.pin.check()); err != nil {
		return err
	}
	current, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if err := source.selection.checkCurrentV1(operation, current); err != nil {
		return err
	}
	if err := source.checkRoleFactsV1(ctx); err != nil {
		return err
	}
	if err := source.launch.checkNativeMemoryV1(ctx, source.launch.manifest); err != nil {
		return err
	}
	return ctx.Err()
}

func (source *custodyKernelSourceV1) checkRoleFactsV1(ctx context.Context) error {
	root, err := source.roles["custody-root-v1"].Stat()
	if err != nil {
		return err
	}
	actual, err := source.operation.native.root.Stat(".")
	if err != nil || !os.SameFile(root, actual) {
		return errors.Join(auxiliaryUnavailableV1("the inherited custody root is not the original OP's exact session"), err)
	}
	if err := checkCustodyFlockV1(source.roles["custody-holder-v1"]); err != nil {
		return err
	}
	if err := checkCustodyOldClosureV1(source.roles["custody-pidfd-v1"], source.roles["custody-cgroup-v1"]); err != nil {
		if err := source.checkPassiveOldClosureV1(ctx); err != nil {
			return err
		}
	}
	claim := &custodyClaimV1{file: source.roles["custody-claim-v1"]}
	if err := claim.peek(); err != nil {
		return err
	}
	entries, err := source.manager.dumpCustodyStoreV1(ctx, UnitName(source.operation.Primary()))
	if err != nil {
		return err
	}
	return verifyCustodyStoreShapeV1(entries, source.roles)
}

func (source *managerCustodyColdV1) Close() error {
	if source == nil || source.custodyKernelSourceV1 == nil {
		return nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil
	}
	if source.launch != nil && !source.launch.ready && !source.cancelled {
		if err := source.cancelBootstrapV1(context.Background()); err != nil {
			return err
		}
	}
	if source.admission != nil && !source.admission.transferred {
		if err := source.admission.cancelV1(context.Background()); err != nil {
			return err
		}
	}
	if source.launch != nil {
		if err := source.launch.Close(); err != nil {
			return err
		}
	}
	if err := source.closeKernelV1(); err != nil {
		return err
	}
	source.closed = true
	return nil
}
