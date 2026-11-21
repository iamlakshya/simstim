package host

import "errors"

import "github.com/iamlakshya/simstim/internal/proto"

// CheckSandboxPrepareCancelV1 checks only the original fixed preparation
// reader and borrowed OP, including after readiness. A manifest marker or a
// historical ready bit cannot issue this control operation.
func (owner *OwnedSupervisorLease) CheckSandboxPrepareCancelV1(line *StartupLineV1) error {
	if owner == nil || line == nil {
		return sessionLeaseError("prepare cancellation lost its original native line")
	}
	if err := line.RejectAncillary(); err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || boot.owner != owner || boot.line != line || owner.pending == nil ||
		owner.pending.line != line || boot.operation == nil || !boot.operation.IsSandboxPrepareBootV1() ||
		!owner.Valid(boot.loan.Primary) || !boot.operation.Valid(boot.loan.Primary) {
		return sessionLeaseError("prepare cancellation belongs to another or ended native owner")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	return errors.Join(owner.pending.peer.CheckSender(sender), checkBorrowedFlockV1(boot.operation.native.file, sender))
}

// ReplySandboxPrepareCancelledReadyV1 settles only this private boot's actual
// registered ready request before its final cancel ACK. A boot cancelled before
// readiness cannot leave that earlier response permanently queued as unknown.
func (owner *OwnedSupervisorLease) ReplySandboxPrepareCancelledReadyV1(line *StartupLineV1) error {
	if owner == nil || line == nil {
		return sessionLeaseError("prepare cancellation lost its original readiness reader")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || boot.owner != owner || boot.line != line || boot.operation == nil || !boot.operation.IsSandboxPrepareBootV1() ||
		owner.native == nil || owner.native.id != boot.loan.Primary || !boot.operation.Valid(boot.loan.Primary) {
		return sessionLeaseError("prepare cancellation lost its original private pending ready request")
	}
	// Construction was cancelled. Check the SAME original native holder
	// through this still-retained borrowed OP's existing cleanup lifetime;
	// this failure reply grants no new start or execution permission.
	if err := owner.native.check(boot.operation.lifetime); err != nil {
		return err
	}
	if boot.readyRequest == 0 || boot.readyReplied {
		return nil
	}
	failure := UserError("sandbox_boot", "the original fixed test was cancelled before readiness", "Run host doctor before retrying the fixed test.")
	if err := line.WriteFrame(proto.TResponse, proto.Response{ID: boot.readyRequest, Error: failure}); err != nil {
		return err
	}
	boot.readyReplied = true
	return nil
}
