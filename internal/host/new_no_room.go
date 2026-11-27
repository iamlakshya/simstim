package host

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureNewNoRoomSettlementV1) }

// Only the actual known-room branch under the native placement gate creates
// this typed result. An ordinary no_room error is not this negative witness.
type newNativeRoomRefusalV1 struct {
	operation *SessionOperation
	facts     memoryRoomFactsV1
	actual    error
}

func (e *newNativeRoomRefusalV1) Error() string { return e.actual.Error() }
func (e *newNativeRoomRefusalV1) Unwrap() error { return e.actual }

func newNativeRoomRefusalFromGateV1(operation *SessionOperation, facts memoryRoomFactsV1) error {
	return &newNativeRoomRefusalV1{operation: operation, facts: facts, actual: noMemoryRoomV1(facts)}
}

// newNoRoomAttemptV1 belongs to one actual captured bridge New, its newly
// created reservation and original native OP. It cannot adopt a saved record.
// terminal is set under operation.gate before any later launch can register.
type newNoRoomAttemptV1 struct {
	witness     *newRequestWitnessV1
	operation   *SessionOperation
	reservation *sessionReservation
	terminal    atomic.Bool
}

type pendingNewNoRoomV1 struct {
	attempt *newNoRoomAttemptV1
	refusal *newNativeRoomRefusalV1
}

func (e *pendingNewNoRoomV1) Error() string { return e.refusal.Error() }
func (e *pendingNewNoRoomV1) Unwrap() error { return e.refusal }

// Called once after actual reservation and OP creation. The process-wall
// path receives a terminal-refusal-only binding, not a runtime receipt.
func bindNewNoRoomAttemptV1(ctx context.Context, operation *SessionOperation, reservation *sessionReservation) error {
	if ctx == nil || operation == nil || reservation == nil {
		return sessionLeaseError("New refusal binding has no original reservation")
	}
	witness, _ := ctx.Value(newRequestKeyV1{}).(*newRequestWitnessV1)
	if witness == nil || witness.attempt == "" {
		return nil
	}
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if !witness.captured || witness.bridge == nil || witness.bridge.ctx.Err() != nil ||
		!api.ValidNewAttemptNonceV1(witness.attempt) || !ValidHostID(witness.hostID) ||
		witness.requestID == 0 || witness.failureAttempt != nil || witness.written ||
		witness.bound != nil && witness.bound != operation || operation.ownerContext.Value(newRequestKeyV1{}) != witness ||
		operation.Primary() != reservation.id || reservation.launched || !operation.Valid(reservation.id) {
		return sessionLeaseError("New refusal binding lost its original request or operation")
	}
	var original NewArgs
	if json.Unmarshal(witness.encoded, &original) != nil || original.ID != reservation.id {
		return sessionLeaseError("New refusal binding names another member")
	}
	if err := operation.native.rootRef.CheckRoot(reservation.dir); err != nil {
		return err
	}
	attempt := &newNoRoomAttemptV1{witness: witness, operation: operation, reservation: reservation}
	witness.failureAttempt, operation.newNoRoom = attempt, attempt
	return nil
}

// The sole call site is the direct first-save refusal BEFORE native startup.
func sealNewNoRoomBeforeDispatchV1(operation *SessionOperation, reservation *sessionReservation, err error) error {
	var refusal *newNativeRoomRefusalV1
	if operation == nil || !errors.As(err, &refusal) || refusal.operation != operation || operation.newNoRoom == nil {
		return err
	}
	attempt := operation.newNoRoom
	witness := attempt.witness
	witness.mu.Lock()
	defer witness.mu.Unlock()
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if attempt.operation != operation || attempt.reservation != reservation || witness.failureAttempt != attempt ||
		witness.bound != nil && witness.bound != operation || witness.written || !witness.captured ||
		!operation.active.Load() || operation.startup != nil || reservation.launched ||
		attempt.terminal.Load() || !refusal.facts.Known || refusal.facts.Needed <= refusal.facts.Room {
		return err
	}
	if check := operation.native.check(context.Background()); check != nil {
		return errors.Join(err, check)
	}
	attempt.terminal.Store(true)
	return &pendingNewNoRoomV1{attempt: attempt, refusal: refusal}
}

func checkNewNoRoomTerminalV1(operation *SessionOperation) error {
	if operation != nil && operation.newNoRoom != nil && operation.newNoRoom.terminal.Load() {
		return sessionLeaseError("the original New was terminally refused before primary dispatch")
	}
	return nil
}

// The actual outer New owner calls this instead of its old void cleanup.
// The report is produced only AFTER exact cleanup and actual native closure.
func finishReservedNewNoRoomV1(operation *SessionOperation, reservation *sessionReservation, original error) error {
	var pending *pendingNewNoRoomV1
	if !errors.As(original, &pending) || pending.attempt == nil || pending.attempt.operation != operation ||
		pending.attempt.reservation != reservation || !pending.attempt.terminal.Load() {
		reservation.close()
		return errors.Join(original, operation.CloseWithError())
	}
	cleanupErr := closeNewNoRoomReservationV1(reservation)
	closeErr := operation.CloseWithError()
	if cleanupErr != nil || closeErr != nil || operation.active.Load() {
		return UserError("start_cleanup", "the refused start has not completed its original cleanup", "Keep the original attempt and inspect this host before trying another.")
	}
	witness := pending.attempt.witness
	witness.mu.Lock()
	defer witness.mu.Unlock()
	if witness.failureAttempt != pending.attempt || witness.bridge == nil || witness.bridge.ctx.Err() != nil ||
		witness.written || operation.startup != nil {
		return original
	}
	settlement := api.NewNoRoomSettlementV1{Version: 1, RequestID: witness.requestID, SessionID: reservation.id,
		HostID: witness.hostID, Attempt: witness.attempt, Outcome: "never-dispatched"}
	if settlement.Validate() != nil {
		return original
	}
	var failure *proto.Error
	if !errors.As(pending.refusal.actual, &failure) || failure.Code != "no_room" {
		return original
	}
	copy := *failure
	copy.Data, _ = json.Marshal(api.NewNoRoomEnvelopeV1{Settlement: settlement, Room: failure.Data})
	return &copy
}
