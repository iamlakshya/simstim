//go:build linux

package host

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
)

func (stopped *managerStoppedV1) checkNativeV1(ctx context.Context) error {
	owner := stopped.capture
	if owner == nil || owner.invocation == nil || owner.invocation.runtime == nil || owner.manager == nil || len(stopped.roles) != custodyBundleSizeV1 {
		return auxiliaryUnavailableV1("manager settlement lost its actual original native objects")
	}
	if err := errors.Join(owner.manager.checkNativeManager(), owner.operation.native.check(ctx), owner.invocation.runtime.service.check(), owner.invocation.runtime.pin.check()); err != nil {
		return err
	}
	if BootID() != owner.boot {
		return auxiliaryUnavailableV1("the original manager-stopped boot epoch changed")
	}
	if err := checkCustodyOldClosureV1(stopped.roles["custody-pidfd-v1"], stopped.roles["custody-cgroup-v1"]); err != nil {
		return err
	}
	if err := checkCustodyFlockV1(stopped.roles["custody-holder-v1"]); err != nil {
		return err
	}
	if err := (&custodyClaimV1{file: stopped.roles["custody-claim-v1"]}).peek(); err != nil {
		return err
	}
	for _, item := range []struct{ iface, name, want string }{{systemdUnitInterface, "Id", owner.invocation.unit}, {systemdUnitInterface, "LoadState", "loaded"}, {systemdUnitInterface, "ActiveState", "inactive"}, {systemdUnitInterface, "SubState", "dead-resources-pinned"}} {
		value, err := owner.manager.getProperty(ctx, owner.invocation.path, item.iface, item.name)
		if err != nil {
			return err
		}
		if got, ok := value.Value().(string); !ok || got != item.want {
			return auxiliaryUnavailableV1("the original manager service is not its exact processless custody state")
		}
	}
	entries, err := owner.manager.dumpCustodyStoreV1(ctx, owner.invocation.unit)
	if err != nil {
		return err
	}
	if err := verifyCustodyStoreShapeV1(entries, stopped.roles); err != nil {
		return err
	}
	aux, err := AuxUnitNameV1(owner.operation.Primary())
	if err != nil {
		return err
	}
	if err := owner.manager.requireAuxiliaryUnitAbsentV1(ctx, aux); err != nil {
		return err
	}
	auxiliary, err := readAuxMemoryUnitV1(ctx, owner.operation.Primary())
	if err != nil || auxiliary.exists {
		return errors.Join(auxiliaryUnavailableV1("the manager stop still has an auxiliary native lifetime"), err)
	}
	primary, err := readMemoryUnitV1(ctx, owner.operation.Primary(), true)
	if err != nil {
		return err
	}
	if primary.exists {
		current, err := openNativeStopCgroupV1(owner.operation.Primary())
		if err != nil {
			return err
		}
		original, statErr := stopped.roles["custody-cgroup-v1"].Stat()
		actual, otherErr := current.Stat()
		empty, emptyErr := originalStopCgroupEmptyV1(current)
		closeErr := current.Close()
		if statErr != nil || otherErr != nil || emptyErr != nil || closeErr != nil || !empty || !os.SameFile(original, actual) {
			return errors.Join(auxiliaryUnavailableV1("the canonical cgroup changed after the original manager stop"), statErr, otherErr, emptyErr, closeErr)
		}
	}
	if err := owner.manager.checkCustodyPinV1(ctx, owner.invocation.unit); err != nil {
		return err
	}
	value, err := owner.manager.getProperty(ctx, owner.invocation.path, systemdUnitInterface, "InvocationID")
	if err != nil {
		return err
	}
	invocation, ok := value.Value().([]byte)
	if !ok || len(invocation) != 16 || hex.EncodeToString(invocation) != owner.invocation.properties.InvocationID {
		return auxiliaryUnavailableV1("the original manager service was reactivated during stop settlement")
	}
	return errors.Join(owner.manager.checkNativeManager(), ctx.Err())
}
