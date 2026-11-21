//go:build linux

package host

import (
	"context"
	"errors"
)

type auxiliaryAccountingOriginKeyV1 struct{}

// requireAuxiliaryAccountingOriginV1 is safe inside the SAME placement gate.
// It checks only the actual private native owner and retained descriptors;
// the enclosing publication gate validates its Active record. It never takes
// another control lock, returns a capability or derives one from a token/JSON.
func requireAuxiliaryAccountingOriginV1(ctx context.Context, operation *SessionOperation) error {
	if ctx == nil || operation == nil {
		return auxiliaryUnavailableV1("auxiliary accounting has no original native operation")
	}
	owner, _ := ctx.Value(auxiliaryAccountingOriginKeyV1{}).(*auxiliaryOperationV1)
	if owner == nil || owner.operation != operation || owner.vacancy == nil || owner.vacancy.operation != operation || owner.vacancy.holder == nil || owner.vacancy.manager == nil || owner.artifacts == nil || owner.artifacts.vacancy != owner.vacancy || owner.artifacts.placement == nil || owner.publication == nil || owner.publication.operation == nil || owner.publication.ctx == nil || owner.publication.owner == nil || owner.publication.operation.record.Kind != "auxiliary" || owner.publication.owner != owner.artifacts.placement || owner.publication.ctx.Err() != nil {
		return auxiliaryUnavailableV1("auxiliary accounting has no actual live native issuer")
	}
	owner.mu.Lock()
	closed := owner.closed
	owner.mu.Unlock()
	if closed || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return auxiliaryUnavailableV1("the original auxiliary accounting owner ended")
	}
	return errors.Join(owner.vacancy.holder.check(ctx), operation.native.rootRef.CheckRoot(owner.vacancy.holder.root), owner.vacancy.manager.checkNativeManager(), owner.artifacts.placement.checkIdentity(), ctx.Err())
}
