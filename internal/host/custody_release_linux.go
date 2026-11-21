//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This is a send-boundary classifier, never a source or admission issuer.
// Its callers already hold the original placement/native gate and have closed
// every outside proof alias. A partial send may release the main permanently.
func (launch *startupLaunchV1) sendCustodyReleaseV1() error {
	if launch == nil || launch.line == nil || launch.custodyReleaseSent {
		return sessionLeaseError("the original custody release was already sent or lost its physical line")
	}
	launch.custodyReleaseSent = true
	return launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 31, Op: "manager_custody_release_v1"})
}

func (owner *OwnedSupervisorLease) checkCustodyReleaseV1(ctx context.Context, boot *originalBootV1, inherited *custodyInheritanceV1) error {
	if owner == nil || owner.custody != inherited || inherited == nil || !inherited.consumed.Load() || inherited.released.Load() || inherited.closed.Load() || boot == nil || owner.startup != boot || boot.owner != owner || boot.operation == nil || !boot.operation.Valid(owner.native.id) || owner.native.borrowed || boot.loan.Purpose != "manager-custody" {
		return sessionLeaseError("the final release has no original claimed main and accepted same-line operation")
	}
	if err := errors.Join(owner.CheckManagerCustodyRequesterV1(ctx), owner.native.check(ctx), checkCustodyFlockV1(owner.native.file), inherited.checkPassiveOldClosureV1(ctx), inherited.manager.checkNativeManager()); err != nil {
		return err
	}
	current, err := inherited.manager.activation(ctx, inherited.path, UnitName(owner.native.id))
	if err != nil || !sameUnitActivationProperties(current, inherited.activation) {
		return errors.Join(sessionLeaseError("the claimed main changed before its original release"), err)
	}
	entries, err := inherited.manager.dumpCustodyStoreV1(ctx, UnitName(owner.native.id))
	if err != nil || len(entries) != 0 {
		return errors.Join(sessionLeaseError("native manager aliases reappeared before final release"), err)
	}
	m, err := boot.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtime, err := startupRuntimeBytes(m)
	if err != nil || startupDigest(m.VMNeedV1) != boot.loan.Need || startupDigest(runtime) != boot.loan.Runtime || m.Status.State != StateStarting {
		return errors.Join(sessionLeaseError("the final selected same-operation restart changed before release"), err)
	}
	return errors.Join(ValidateManifestStart(m), ctx.Err())
}

// This observes the actual inherited main, accepted original OP and real
// native release. It cannot be issued by a start envelope or context flag.
func (owner *OwnedSupervisorLease) ManagerCustodyReleasedV1() bool {
	if owner == nil || owner.custody == nil || owner.native == nil || !owner.custody.consumed.Load() || !owner.custody.released.Load() || owner.custody.closed.Load() {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	return boot != nil && boot.owner == owner && boot.loan.Purpose == "manager-custody" && boot.operation != nil && boot.operation.Valid(owner.native.id) && owner.Valid(owner.native.id)
}

func (owner *OwnedSupervisorLease) CheckManagerCustodyCancelV1(line *StartupLineV1) error {
	if !owner.ManagerCustodyReleasedV1() {
		return sessionLeaseError("the pending manager cancellation has no actual released original bootstrap")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup == nil || owner.startup.line != line || owner.startup.ready || owner.pending == nil || owner.pending.line != line {
		return sessionLeaseError("the pending manager cancellation changed its original physical line or committed")
	}
	return owner.pending.peer.Check()
}

// The original borrower can stop this passive stack without asking systemd
// to create a second job. Only the concrete pre-release protocol admits it.
func (launch *startupLaunchV1) abortCustodyPassiveV1() error {
	if launch == nil || launch.line == nil || launch.custodyReleaseSent {
		return sessionLeaseError("the passive custody abort crossed a possible factory release")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := launch.line.WriteFrame(proto.TRequest, proto.Request{ID: 29, Op: "manager_custody_abort_v1"}); err != nil {
		return err
	}
	_, err := launch.response(ctx, 29, false)
	return err
}
