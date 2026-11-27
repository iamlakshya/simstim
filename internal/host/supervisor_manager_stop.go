package host

import "context"

// managerStopSourceV1 is closed to this package's actual native producers.
// It accepts no callback, DTO, source selector or descriptor setter.
type managerStopSourceV1 interface {
	checkManagerStopOwnerV1(context.Context, *OwnedSupervisorLease) error
	retainManagerStopOwnerV1(context.Context, *OwnedSupervisorLease) error
}

// bindManagerStopSourceV1 is called only after the persistent runtime's
// concrete original native object capture. Merely having a unit is not a
// constructor, and an unbound managed runtime cannot take the legacy path.
func (owner *OwnedSupervisorLease) bindManagerStopSourceV1(ctx context.Context, source managerStopSourceV1) error {
	if owner == nil || source == nil {
		return sessionLeaseError("the persistent manager stop has no original concrete owner")
	}
	if err := source.checkManagerStopOwnerV1(ctx, owner); err != nil {
		return err
	}
	owner.mu.Lock()
	if owner.managerStop != nil || owner.native == nil || !owner.Valid(owner.native.id) {
		owner.mu.Unlock()
		return sessionLeaseError("the original persistent manager stop owner was already bound or ended")
	}
	owner.managerStop = source
	owner.mu.Unlock()
	return source.checkManagerStopOwnerV1(ctx, owner)
}

// RetainManagerStopForSameRequestV1 preserves an actual registered persistent
// manager owner. Without one it proves the original transient native path,
// rather than interpreting nil or a stopped row as a custody receipt.
func (owner *OwnedSupervisorLease) RetainManagerStopForSameRequestV1(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return sessionLeaseError("the retained stop has no actual original supervisor owner")
	}
	owner.mu.Lock()
	source := owner.managerStop
	owner.mu.Unlock()
	if source == nil {
		return owner.checkOriginalTransientManagerStopV1(ctx)
	}
	if err := source.checkManagerStopOwnerV1(ctx, owner); err != nil {
		return err
	}
	return source.retainManagerStopOwnerV1(ctx, owner)
}
