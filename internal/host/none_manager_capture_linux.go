//go:build linux

package host

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
)

// This is a distinct host-account source. It owns the original manager's
// five kernel aliases and an actual ordinary RSS group, never VM artefacts,
// a declared quantum, or a newly acquired vacancy holder.
type noneManagerCustodyV1 struct {
	self *noneManagerCustodyV1
	cold *NoneColdResumeV1
	*custodyKernelSourceV1
	rssRegistered  bool
	aliasesRemoved bool
}

func captureManagedNoneColdV1(ctx context.Context, operation *SessionOperation, p *bridgeResumePreparationV1, before *Manifest) (result *NoneColdResumeV1, failure error) {
	if p == nil || p.operation != operation || !p.active.Load() || p.bridge == nil || p.lifetime == nil || p.lifetime.Err() != nil || !noneColdRoleV1(before) || before.Run.BootID != BootID() || before.Run.Unit != UnitName(operation.Primary()) || !validNoneCleanupV1(before, BootID()) {
		return nil, sessionLeaseError("the managed None source has no original current cleanup and prepare requester")
	}
	registered, _ := ctx.Value(bridgeResumePreparationContextKeyV1{}).(*bridgeResumePreparationV1)
	if registered != p {
		return nil, sessionLeaseError("the managed None capture has another original requester context")
	}
	p.bridge.mu.Lock()
	service := p.bridge.resumePreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return nil, sessionLeaseError("the managed None capture lost its actual registered request")
	}
	kernel, err := captureCustodyKernelSourceV1(ctx, operation, before)
	if kernel == nil {
		return nil, err
	}
	cold := &NoneColdResumeV1{operation: operation, preparation: p, source: CloneManifest(*before), boot: BootID(), sameBoot: true}
	cold.self = cold
	managed := &noneManagerCustodyV1{cold: cold, custodyKernelSourceV1: kernel}
	managed.self = managed
	cold.managed = managed
	complete := false
	defer func() {
		if !complete {
			cleanup := managed.closeUnstartedV1()
			failure = errors.Join(failure, cleanup)
			if cleanup != nil {
				result = cold
			}
		}
	}()
	if err != nil {
		return nil, err
	}
	cold.memberIDs, err = captureNoneColdMemberIDsV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := cold.captureMetadataV1(ctx, nil); err != nil {
		return nil, err
	}
	cold.originalBytes = bytes.Clone(cold.metadataBytes)
	if err := managed.checkDormantV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return cold, nil
}

func (managed *noneManagerCustodyV1) checkIdentityV1(ctx context.Context) error {
	if managed == nil || managed.self != managed || managed.cold == nil || managed.cold.self != managed.cold || managed.cold.managed != managed || managed.custodyKernelSourceV1 == nil || managed.operation != managed.cold.operation || managed.closed || managed.boot != BootID() || !managed.operation.Valid(managed.operation.Primary()) || managed.operation.native.borrowed {
		return sessionLeaseError("the original managed None kernel source ended or changed")
	}
	if !noneColdRoleV1(&managed.source) || !noneColdRoleV1(&managed.cold.source) || !validNoneCleanupV1(&managed.cold.source, managed.boot) {
		return sessionLeaseError("the managed None continuation lost its original host-account cleanup role")
	}
	return errors.Join(CheckCaller(), managed.origin.checkIdentity(), managed.operation.native.check(ctx), managed.manager.checkNativeManager(), ctx.Err())
}

func (managed *noneManagerCustodyV1) checkDormantV1(ctx context.Context) error {
	if err := managed.checkIdentityV1(ctx); err != nil {
		return err
	}
	if managed.launch != nil || managed.claimed || managed.cancelled {
		return sessionLeaseError("the managed None source is no longer dormant")
	}
	current, err := managed.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if current.Limits != managed.source.Limits || !noneColdRoleV1(current) || !reflect.DeepEqual(current.Wall, managed.source.Wall) {
		return sessionLeaseError("the dormant managed None role or execution limit changed")
	}
	return errors.Join(managed.checkPinnedV1(ctx), managed.runtime.service.check(), managed.runtime.pin.check())
}

// The caller holds the actual None continuation mutex. This dispatches only
// between its two concrete native producer classes; neither becomes vacancy.
func (cold *NoneColdResumeV1) checkBeforeNativeV1(ctx context.Context) error {
	if cold.managed != nil {
		return cold.managed.checkPreparedV1(ctx)
	}
	return cold.checkVacancyV1(ctx)
}

func (managed *noneManagerCustodyV1) checkPreparedV1(ctx context.Context) error {
	if err := managed.checkIdentityV1(ctx); err != nil {
		return err
	}
	if managed.launch == nil {
		return managed.checkDormantV1(ctx)
	}
	if !managed.claimed {
		if err := managed.cold.checkMetadataV1(ctx); err != nil {
			return err
		}
	}
	if managed.cancelled {
		return sessionLeaseError("the managed None bootstrap already settled for cleanup")
	}
	if managed.claimed {
		return managed.checkTakeoverV1(ctx)
	}
	if !managed.rssRegistered || len(managed.roles) != custodyBundleSizeV1 {
		return sessionLeaseError("the managed None bootstrap has no complete inherited kernel and actual RSS owners")
	}
	if err := managed.launch.check(ctx); err != nil {
		return err
	}
	if err := managed.checkRoleFactsV1(ctx); err != nil {
		return err
	}
	if err := managed.checkOriginalHolderV1(ctx); err != nil {
		return err
	}
	return managed.checkRSSV1(ctx)
}

func (managed *noneManagerCustodyV1) checkOriginalHolderV1(ctx context.Context) error {
	if managed == nil || managed.custodyKernelSourceV1 == nil || managed.cold == nil || managed.cold.source.NoneCleanup == nil {
		return sessionLeaseError("the managed None holder source is absent")
	}
	file := managed.roles["custody-holder-v1"]
	if file == nil || managed.roles["custody-root-v1"] == nil {
		return sessionLeaseError("the managed None source lost the original inherited holder OFD")
	}
	if err := errors.Join(managed.cold.source.NoneCleanup.Directory.CheckRoot(managed.operation.native.root), managed.cold.source.NoneCleanup.Holder.CheckFile(managed.operation.native.root, holderLockFile, file, 0600), checkCustodyFlockV1(file)); err != nil {
		return err
	}
	root, err := managed.roles["custody-root-v1"].Stat()
	if err != nil {
		return err
	}
	actual, err := managed.operation.native.root.Stat(".")
	if err != nil || !os.SameFile(root, actual) {
		return errors.Join(sessionLeaseError("the managed None holder and operation have different original roots"), err)
	}
	return ctx.Err()
}

func (managed *noneManagerCustodyV1) closeUnstartedV1() error {
	if managed == nil || managed.self != managed || managed.custodyKernelSourceV1 == nil {
		return sessionLeaseError("the managed None cleanup has no original native source")
	}
	if managed.launch != nil {
		return sessionLeaseError("a dispatched managed None source must join its actual creator first")
	}
	if managed.cold.metadata != nil {
		if err := managed.cold.metadata.Close(); err != nil {
			return err
		}
		managed.cold.metadata = nil
	}
	return managed.closeKernelV1()
}

func (cold *NoneColdResumeV1) checkRetainedHolderV1(ctx context.Context) error {
	if cold.managed != nil {
		return cold.managed.checkRetainedCleanupV1(ctx)
	}
	if cold.holder == nil {
		return sessionLeaseError("the original None holder is absent")
	}
	return cold.holder.check(ctx)
}
