//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (managed *noneManagerCustodyV1) checkRestartRSSLockedV1(ctx context.Context, next *Manifest, control *placementControl) error {
	if managed == nil || managed.custodyKernelSourceV1 == nil {
		return sessionLeaseError("the managed None RSS source is absent")
	}
	cold := managed.cold
	if control == nil || cold == nil || !cold.bound || cold.handed || cold.closed || managed.claimed || managed.cancelled || !managed.rssRegistered || managed.operation.noneCold != cold || next == nil || !noneColdRoleV1(next) || next.Limits.Declared != 0 || managed.launch == nil || next.Limits != managed.launch.manifest.Limits {
		return sessionLeaseError("the managed None restart changed its original ordinary execution limit or RSS owner")
	}
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	if err := managed.checkPreparedV1(ctx); err != nil {
		return err
	}
	if err := checkUserDrainLockedV1(ctx, control); err != nil {
		return err
	}
	if err := checkAdmittedRestartCurrentV1(managed.operation, next); err != nil {
		return err
	}
	facts, err := observeMemoryRoomLockedV1(ctx, "", 0)
	if err != nil {
		return err
	}
	if facts.Room < 0 {
		return noMemoryRoomV1(facts)
	}
	return nil
}

func checkManagedNoneRSSStartLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest, control *placementControl) (bool, error) {
	if operation == nil || operation.noneCold == nil || operation.noneCold.managed == nil {
		return false, nil
	}
	managed := operation.noneCold.managed
	if managed.claimed {
		return true, managed.checkTakeoverV1(ctx)
	}
	return true, managed.checkRestartRSSLockedV1(ctx, next, control)
}

func (managed *noneManagerCustodyV1) launchFinalV1(ctx context.Context, operation *SessionOperation, m *Manifest) (*startupLaunchV1, error) {
	cold := managed.cold
	cold.mu.Lock()
	if cold.closed || cold.handed || !cold.bound || operation.noneCold != cold || managed.claimed || managed.cancelled || managed.launch == nil || !managed.rssRegistered {
		cold.mu.Unlock()
		return nil, sessionLeaseError("the original managed None source cannot start another runtime")
	}
	if err := managed.checkPreparedV1(ctx); err != nil {
		cold.mu.Unlock()
		return nil, err
	}
	publication, err := beginStartupPublication(operation.ownerContext, operation)
	if err != nil {
		cold.mu.Unlock()
		return nil, err
	}
	launch := managed.launch
	previous := launch.placement
	operation.gate.Lock()
	if !operation.active.Load() || operation.startup != nil {
		operation.gate.Unlock()
		cold.mu.Unlock()
		_ = publication.close()
		return nil, sessionLeaseError("the original None operation already owns another creation")
	}
	operation.startup = launch
	launch.placement = publication
	launch.manifest = m
	cold.startup = launch
	operation.gate.Unlock()
	cold.mu.Unlock()
	if err := previous.close(); err != nil {
		return launch, err
	}
	err = publication.gateControl(ctx, func(control *placementControl) error {
		cold.mu.Lock()
		defer cold.mu.Unlock()
		if err := managed.checkRestartRSSLockedV1(ctx, m, control); err != nil {
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
		managed.claimed = true
		cold.handed = true
		return launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 30, Op: "manager_custody_claim_v1", Args: selection})
	})
	if err != nil {
		return launch, err
	}
	if _, err := launch.response(ctx, 30, true); err != nil {
		return launch, err
	}
	entries, err := managed.manager.dumpCustodyStoreV1(ctx, UnitName(operation.Primary()))
	if err != nil || len(entries) != 0 {
		return launch, errors.Join(sessionLeaseError("the managed None claim retained manager aliases"), err)
	}
	cold.mu.Lock()
	if err := managed.closeProofAliasesV1(); err != nil {
		cold.mu.Unlock()
		return launch, err
	}
	managed.aliasesRemoved = true
	cold.mu.Unlock()
	if err := publication.gate(ctx, func() error {
		if err := managed.checkTakeoverV1(ctx); err != nil {
			return err
		}
		return launch.sendCustodyReleaseV1()
	}); err != nil {
		return launch, err
	}
	if _, err := launch.response(ctx, 31, true); err != nil {
		return launch, err
	}
	return launch, nil
}

func (managed *noneManagerCustodyV1) checkTakeoverV1(ctx context.Context) error {
	if managed == nil || managed.custodyKernelSourceV1 == nil {
		return sessionLeaseError("the managed None takeover source is absent")
	}
	cold := managed.cold
	launch := managed.launch
	if !managed.claimed || !managed.aliasesRemoved || len(managed.roles) != 0 || cold == nil || !cold.bound || !cold.handed || cold.closed || cold.operation.noneCold != cold || cold.startup != launch || managed.operation.startup != launch || launch == nil || launch.line == nil || managed.creation == nil || managed.group == nil {
		return sessionLeaseError("the managed None continuation has no original claimed native takeover")
	}
	if err := managed.checkIdentityV1(ctx); err != nil {
		return err
	}
	if err := managed.checkRSSV1(ctx); err != nil {
		return err
	}
	entries, err := managed.manager.dumpCustodyStoreV1(ctx, UnitName(managed.operation.Primary()))
	if err != nil || len(entries) != 0 {
		return errors.Join(sessionLeaseError("the original None manager regained custody aliases after claim"), err)
	}
	current, err := managed.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !noneColdRoleV1(current) || current.Limits != launch.manifest.Limits {
		return sessionLeaseError("the actual managed None role or execution limit changed during takeover")
	}
	return errors.Join(launch.check(ctx), ctx.Err())
}

// This is ordinary execution-limit/RSS admission. It never creates a VM
// reservation or exposes a saved numeric RSS value as authority.
func reserveManagedNoneRestartRSSLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest, control *placementControl) (bool, error) {
	handled, err := checkManagedNoneRSSStartLockedV1(ctx, operation, next, control)
	if handled && err == nil {
		next.Run.BootID = BootID()
		next.Status.State = StateStarting
		next.Status.Since = time.Now().UTC()
	}
	return handled, err
}
