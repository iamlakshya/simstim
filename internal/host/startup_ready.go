package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// StartupReadyOperationV1 is served only on the accepted original startup
// connection. It carries readiness data and grants no later control or stop.
const StartupReadyOperationV1 = "startup_ready_v1"

// ReplyStartupLoan acknowledges only the actual accepted native OP on the
// same original line. The helper never acknowledges a raw correlation object.
func (owner *OwnedSupervisorLease) ReplyStartupLoan(line *StartupLineV1, requestID uint64, operation *SessionOperation) error {
	if owner == nil || line == nil || operation == nil || requestID == 0 {
		return sessionLeaseError("startup cannot acknowledge a missing original loan")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup == nil || owner.pending == nil || owner.startup.operation != operation || owner.startup.line != line ||
		!owner.Valid(operation.Primary()) || !operation.Valid(operation.Primary()) {
		return sessionLeaseError("startup cannot acknowledge another operation or connection")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := errors.Join(owner.pending.peer.CheckSender(sender), checkBorrowedFlockV1(operation.native.file, sender)); err != nil {
		return err
	}
	return line.WriteFrame(proto.TResponse, proto.Response{ID: requestID, OK: true, Result: startupReply(owner.startup.loan.Nonce, "")})
}

// RegisterStartupReady records one reply on the original physical reader and
// returns immediately. Pending cancellation stays readable while boot runs.
// CommitStartupReady flushes this reply only after the real durable commit.
func (owner *OwnedSupervisorLease) RegisterStartupReady(line *StartupLineV1, requestID uint64) error {
	if owner == nil || line == nil || requestID == 0 {
		return sessionLeaseError("startup readiness has no original request")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.startup == nil || owner.pending == nil || owner.startup.line != line || owner.startup.readyRequest != 0 ||
		!owner.Valid(owner.startup.loan.Primary) {
		return sessionLeaseError("startup readiness belongs to another owner or was already requested")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := owner.pending.peer.CheckSender(sender); err != nil {
		return err
	}
	owner.startup.readyRequest = requestID
	if owner.startup.ready {
		return owner.replyReadyLocked()
	}
	return nil
}

// CommitStartupReady verifies the fresh durable protected record written by
// this same native owner. The supervisor invokes it after durable Save and
// port reconciliation, before replay/ack. It closes only the borrowed OP copy;
// the original outside owner retains its own copy through the acknowledgement.
func (owner *OwnedSupervisorLease) CommitStartupReady(ctx context.Context, operation *SessionOperation) error {
	if owner == nil || operation == nil || ctx == nil {
		return sessionLeaseError("the ready commit has no original startup scope")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || boot.operation != operation || boot.owner != owner || boot.ready || !owner.Valid(operation.Primary()) ||
		!operation.Valid(operation.Primary()) {
		return sessionLeaseError("the ready commit is not this owner's original pending boot")
	}
	m, err := operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if m.Wall == nil || m.Life == nil || m.Life.Started.IsZero() || m.Run.Generation != owner.generation ||
		m.Run.HolderPID != os.Getpid() || m.Run.BootID != BootID() || m.Run.Socket != paths.Socket(operation.Primary()) {
		return sessionLeaseError("the actual native supervisor has no matching durable ready publication")
	}
	if err := operation.CloseWithError(); err != nil {
		return err
	}
	boot.ready = true
	if boot.readyRequest != 0 {
		return owner.replyReadyLocked()
	}
	return nil
}

func (owner *OwnedSupervisorLease) replyReadyLocked() error {
	boot := owner.startup
	if boot == nil || !boot.ready || boot.readyRequest == 0 || boot.readyReplied {
		return sessionLeaseError("the original ready acknowledgement is not pending")
	}
	if err := boot.line.WriteFrame(proto.TResponse, proto.Response{ID: boot.readyRequest, OK: true, Result: startupReply(boot.loan.Nonce, owner.generation)}); err != nil {
		return err
	}
	boot.readyReplied = true
	return nil
}

func startupReply(nonce, generation string) []byte {
	// Both values are private producer-generated lower-case hex. Encoding
	// through the canonical JSON writer still owns the final frame envelope.
	if generation == "" {
		return []byte(`{"startup_nonce":"` + nonce + `"}`)
	}
	return []byte(`{"startup_nonce":"` + nonce + `","generation":"` + generation + `"}`)
}

// CheckStartupFrame checks a selected start frame on the same accepted native
// startup line before its values are consumed. It exposes no credential DTO or
// substitute constructor; the native owner, current sender and held OP remain
// the actual accepted ones.
func (owner *OwnedSupervisorLease) CheckStartupFrame(line *StartupLineV1) error {
	if owner == nil || line == nil {
		return sessionLeaseError("the selected start has no original native owner and line")
	}
	if err := line.RejectAncillary(); err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if boot == nil || owner.pending == nil || boot.line != line || owner.pending.line != line || boot.ready ||
		!owner.Valid(boot.loan.Primary) || !boot.operation.Valid(boot.loan.Primary) {
		return sessionLeaseError("the selected start belongs to another or ended native startup scope")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := owner.pending.peer.CheckSender(sender); err != nil {
		return err
	}
	return checkBorrowedFlockV1(boot.operation.native.file, sender)
}

// StartupCommittedV1 reports the actual durable commit of this same native
// owner. It is historical state data, not an acknowledgement, receipt or grant.
// A failed ACK write after this boundary cannot cancel the committed session.
func (owner *OwnedSupervisorLease) StartupCommittedV1() bool {
	if owner == nil {
		return false
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.startup != nil && owner.startup.ready
}
