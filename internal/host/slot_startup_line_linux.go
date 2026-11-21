//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func receiveSlotReservedStartupAncillaryV2(conn net.Conn, data []byte) (int, []*os.File, *startupCredentials, error) {
	native, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, nil, nil, sessionLeaseError("the reserved startup lost its original Unix channel")
	}
	n, control, err := proto.ReceiveUnixControlV1(native, data, proto.NativeSlotReservedStartupRoleCountV2)
	var credentials *startupCredentials
	if control.Credentials != nil {
		credentials = &startupCredentials{pid: control.Credentials.PID, uid: control.Credentials.UID, gid: control.Credentials.GID}
	}
	return n, control.Files, credentials, err
}

func (owner *OwnedSupervisorLease) PrepareSlotReservedStartupV2(ctx context.Context, line *StartupLineV1, frame proto.Frame) error {
	if owner == nil || owner.native == nil || ctx == nil || line == nil {
		return sessionLeaseError("the reserved startup has no actual native candidate")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.Valid(owner.native.id) || owner.startup != nil || owner.pending != nil {
		return sessionLeaseError("the original supervisor already accepted its startup source")
	}
	if err := line.RejectAncillary(); err != nil {
		return err
	}
	var request proto.Request
	var args slotReservedStartupPrepareV2
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != proto.NativeSlotReservedStartupPrepareRequestV2 || request.Op != proto.NativeSlotReservedStartupPrepareV2 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 2 || args.Primary != owner.native.id {
		return sessionLeaseError("the reserved startup preparation changed its exact closed purpose")
	}
	line.mu.Lock()
	candidate := line.candidate
	valid := !line.closed && !line.reading && candidate != nil && candidate.owner == owner && candidate.line == line && candidate.peer == line.nativePeer && candidate.nonce == args.Nonce && !line.snapshotLoan && !line.snapshotReservation && line.slotStartupPending == nil && len(line.files) == 0 && line.loanFiles == 1
	line.mu.Unlock()
	if !valid {
		return sessionLeaseError("the reserved startup preparation is not this original native candidate")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := candidate.peer.CheckSender(sender); err != nil {
		return err
	}
	pending := &slotReservedStartupPendingV2{owner: owner, line: line, candidate: candidate, preparationFrame: frame, prepare: args}
	line.mu.Lock()
	if line.closed || line.reading || line.candidate != candidate || line.slotStartupPending != nil {
		line.mu.Unlock()
		return sessionLeaseError("the reserved startup candidate ended before its exact reader was armed")
	}
	line.slotStartupPending = pending
	line.mu.Unlock()
	ack, _ := json.Marshal(struct {
		Version int    `json:"v"`
		Nonce   string `json:"startup_nonce"`
	}{2, args.Nonce})
	return line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, OK: true, Result: ack})
}

func (line *StartupLineV1) takeSlotReservedStartupV2() ([]*os.File, startupCredentials, error) {
	line.mu.Lock()
	defer line.mu.Unlock()
	if line.closed || line.reading || line.slotStartupPending == nil || line.snapshotLoan || line.snapshotReservation || len(line.files) != proto.NativeSlotReservedStartupRoleCountV2 || line.cred == nil || line.controlAt != line.start {
		return nil, startupCredentials{}, errors.Join(sessionLeaseError("the reserved startup has no exact current OP and reservation vector"), line.closeAncillaryLocked())
	}
	files, sender := line.files, *line.cred
	line.files = nil
	return files, sender, nil
}

func (incoming *slotReservedStartupIncomingV2) checkOriginalV2(ctx context.Context) error {
	if incoming == nil || ctx == nil || incoming.owner == nil || incoming.line == nil || incoming.pending == nil || incoming.preparation == nil || incoming.used {
		return sessionLeaseError("the actual reserved startup incoming producer ended or was consumed")
	}
	owner := incoming.owner
	if owner.native == nil || !owner.Valid(owner.native.id) || owner.startup != nil || owner.pending != nil || incoming.preparation.owner != owner || incoming.preparation.line != incoming.line || incoming.preparation.candidate != incoming.pending || incoming.pending.owner != owner || incoming.pending.line != incoming.line || incoming.pending.peer == nil || incoming.pending.nonce != incoming.preparation.prepare.Nonce {
		return sessionLeaseError("the original reserved startup candidate, owner or preparation changed")
	}
	incoming.line.mu.Lock()
	valid := !incoming.line.closed && !incoming.line.reading && incoming.line.candidate == incoming.pending && incoming.line.nativePeer == incoming.pending.peer && incoming.line.slotStartupPending == incoming.preparation
	incoming.line.mu.Unlock()
	if !valid {
		return sessionLeaseError("the reserved startup lost its actual physical candidate line")
	}
	var prepared, loan proto.Request
	var args slotReservedStartupLoanV2
	if incoming.preparationFrame.Type != proto.TRequest || json.Unmarshal(incoming.preparationFrame.Payload, &prepared) != nil || prepared.ID != proto.NativeSlotReservedStartupPrepareRequestV2 || prepared.Op != proto.NativeSlotReservedStartupPrepareV2 || incoming.loanFrame.Type != proto.TRequest || !api.ExactJSONFields(incoming.loanFrame.Payload, loan) || json.Unmarshal(incoming.loanFrame.Payload, &loan) != nil || loan.ID != proto.NativeSlotReservedStartupLoanRequestV2 || loan.Op != proto.NativeSlotReservedStartupLoanV2 || !api.ExactJSONFields(loan.Args, args) || json.Unmarshal(loan.Args, &args) != nil || args.Version != 2 || args.Base.Primary != owner.native.id || args.Base.Nonce != incoming.pending.nonce || args.Base.Need != incoming.preparation.prepare.Need || args.Base.Runtime != incoming.preparation.prepare.Runtime || len(incoming.files) != proto.NativeSlotReservedStartupRoleCountV2 {
		return sessionLeaseError("the reserved startup changed its exact prepare, loan or fixed-role class")
	}
	return errors.Join(ctx.Err(), incoming.pending.peer.CheckSender(incoming.sender))
}

func (incoming *slotReservedStartupIncomingV2) closeUnconsumedV2() (failure error) {
	if incoming == nil {
		return nil
	}
	incoming.mu.Lock()
	defer incoming.mu.Unlock()
	if incoming.used {
		return nil
	}
	for _, file := range incoming.files {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	incoming.files = nil
	return failure
}

func (owner *OwnedSupervisorLease) AcceptSlotReservedStartupV2(ctx context.Context, line *StartupLineV1, frame proto.Frame) (_ *SessionOperation, returned error) {
	if owner == nil || ctx == nil || line == nil {
		return nil, sessionLeaseError("the reserved startup has no actual owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	line.mu.Lock()
	pending := line.slotStartupPending
	line.mu.Unlock()
	if pending == nil {
		return nil, sessionLeaseError("the original reserved startup reader was not prepared")
	}
	defer func() {
		line.mu.Lock()
		if !line.reading && line.slotStartupPending == pending {
			line.slotStartupPending = nil
		}
		line.mu.Unlock()
	}()
	files, sender, err := line.takeSlotReservedStartupV2()
	if err != nil {
		return nil, err
	}
	incoming := &slotReservedStartupIncomingV2{owner: owner, pending: pending.candidate, line: line, preparation: pending, preparationFrame: pending.preparationFrame, loanFrame: frame, sender: sender, files: files}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, incoming.closeUnconsumedV2())
		}
	}()
	if err := incoming.checkOriginalV2(ctx); err != nil {
		return nil, err
	}
	return acceptSlotReservedStartupIncomingV2(ctx, incoming)
}

func (line *StartupLineV1) writeSlotReservedStartupV2(value any, files []*os.File) error {
	if line == nil || len(files) != proto.NativeSlotReservedStartupRoleCountV2 {
		return sessionLeaseError("the reserved startup sender has another fixed native role count")
	}
	connection, ok := line.conn.(*net.UnixConn)
	if !ok {
		return sessionLeaseError("the reserved startup sender lost its original native channel")
	}
	for _, file := range files {
		if file == nil {
			return sessionLeaseError("the reserved startup sender has an absent actual role")
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	line.writeMu.Lock()
	defer line.writeMu.Unlock()
	return proto.SendUnixFrameV1(connection, proto.TRequest, raw, files)
}
