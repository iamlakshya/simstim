//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"golang.org/x/sys/unix"
)

const ordinaryPurgeBindCohortV3 = "ordinary.purge.cohort.v3"

// A holder receives only a native resource projection from its already
// authenticated original peer. This is not a transaction or stop source.
type ordinaryHolderCohortV3 struct {
	receiver, sender             *os.File
	receiverInfo, senderInfo     os.FileInfo
	receiverCookie, senderCookie uint64
}

func receiveOrdinaryHolderCohortV3(line *StartupLineV1, id uint64, state ordinaryStopStateV1) (result *ordinaryHolderCohortV3, returned error) {
	owner := &ordinaryHolderCohortV3{}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.Close())
		}
	}()
	for _, target := range []**os.File{&owner.receiver, &owner.sender} {
		frame, err := line.ReadFrame()
		if err != nil {
			return nil, err
		}
		var request proto.Request
		var actual ordinaryStopStateV1
		if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID != id || request.Op != ordinaryPurgeBindCohortV3 || request.NewAttempt != "" || !api.ExactJSONFields(request.Args, actual) || json.Unmarshal(request.Args, &actual) != nil || actual != state {
			return nil, sessionLeaseError("the original ordinary cohort changed its native admission continuation")
		}
		file, sender, err := line.takeLoanFile()
		if err != nil {
			return nil, err
		}
		*target = file
		if err := line.nativePeer.CheckSender(sender); err != nil {
			return nil, err
		}
	}
	var err error
	owner.receiverInfo, err = owner.receiver.Stat()
	if err != nil {
		return nil, err
	}
	owner.senderInfo, err = owner.sender.Stat()
	if err != nil {
		return nil, err
	}
	owner.receiverCookie, err = unix.GetsockoptUint64(int(owner.receiver.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		return nil, err
	}
	owner.senderCookie, err = unix.GetsockoptUint64(int(owner.sender.Fd()), unix.SOL_SOCKET, unix.SO_COOKIE)
	if err != nil {
		return nil, err
	}
	if err := owner.checkFilesV3(); err != nil {
		return nil, err
	}
	first, err := owner.firstV3()
	if err != nil || first != purgeCohortPendingV3 {
		return nil, errors.Join(sessionLeaseError("the original holder cannot become ready after the native cohort has decided"), err)
	}
	complete = true
	return owner, nil
}

func (owner *ordinaryHolderCohortV3) checkFilesV3() error {
	if owner == nil || owner.receiverCookie == owner.senderCookie {
		return sessionLeaseError("the original ordinary monitor lost its distinct native decision roles")
	}
	return errors.Join(checkPurgeDecisionFileV3(owner.receiver, owner.receiverInfo, owner.receiverCookie), checkPurgeDecisionFileV3(owner.sender, owner.senderInfo, owner.senderCookie))
}

func (owner *ordinaryHolderCohortV3) firstV3() (purgeCohortValueV3, error) {
	if err := owner.checkFilesV3(); err != nil {
		return purgeCohortPendingV3, err
	}
	return peekPurgeCohortDecisionV3(owner.receiver)
}

func (owner *ordinaryHolderCohortV3) abortV3() (purgeCohortValueV3, error) {
	if err := owner.checkFilesV3(); err != nil {
		return purgeCohortPendingV3, err
	}
	return proposePurgeCohortDecisionV3(owner.receiver, owner.sender, purgeCohortAbortV3)
}

func (owner *ordinaryHolderCohortV3) Close() error {
	if owner == nil {
		return nil
	}
	var failure error
	for _, file := range []*os.File{owner.receiver, owner.sender} {
		if file != nil {
			failure = errors.Join(failure, file.Close())
		}
	}
	return failure
}

// bindCohortV3 installs the SAME transaction's original pair before any main
// pause. A partial send retains this actual loan until restoration joins.
func (preparation *OrdinaryPurgePreparationV2) bindCohortV3(ctx context.Context, decision *PurgeCohortDecisionV3) error {
	if preparation == nil || ctx == nil || decision == nil {
		return sessionLeaseError("the original ordinary cohort admission is absent")
	}
	preparation.mu.Lock()
	defer preparation.mu.Unlock()
	if preparation.owner == nil || preparation.owner.managed != nil || preparation.owner.pauseSent || preparation.decision != nil {
		return sessionLeaseError("the original ordinary monitor cannot bind another native cohort")
	}
	if err := preparation.checkOriginalV2(ctx, preparation.owner.operation); err != nil {
		return err
	}
	loan, err := decision.borrowForOperationV3(ctx, preparation.owner.operation)
	if err != nil {
		return err
	}
	preparation.decision = loan
	if preparation.next < 2 || preparation.next == ^uint64(0) {
		return sessionLeaseError("the original ordinary cohort exhausted its native continuation")
	}
	id := preparation.next
	preparation.next++
	line := preparation.owner.line
	deadline := time.Now().Add(10 * time.Second)
	if actual, ok := ctx.Deadline(); ok && actual.Before(deadline) {
		deadline = actual
	}
	_ = line.SetDeadline(deadline)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-done
		}
		_ = line.SetDeadline(time.Time{})
	}()
	raw, err := json.Marshal(preparation.owner.state)
	if err != nil {
		return err
	}
	request := proto.Request{ID: id, Op: ordinaryPurgeBindCohortV3, Args: raw}
	if err := line.WriteFrame(proto.TRequest, request); err != nil {
		return err
	}
	if err := loan.WithNativeDecisionFilesV1(ctx, func(receiver, sender *os.File) error {
		if err := line.writeLoan(request, receiver); err != nil {
			return err
		}
		return line.writeLoan(request, sender)
	}); err != nil {
		return err
	}
	frame, err := line.ReadFrame()
	if err != nil {
		return err
	}
	if err := line.RejectAncillary(); err != nil {
		return err
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := preparation.owner.peer.checkNativeSender(sender); err != nil {
		return err
	}
	var response proto.Response
	var actual ordinaryStopStateV1
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id || !response.OK || response.Error != nil || !api.ExactJSONFields(response.Result, actual) || json.Unmarshal(response.Result, &actual) != nil || actual != preparation.owner.state {
		return sessionLeaseError("the actual original holder did not acknowledge its same native cohort admission")
	}
	preparation.decisionReady = true
	return nil
}

// The original holder itself performs commit and produces its existing real
// main/PTY/VT join. No stream command or local phase selects the decision.
func (preparation *OrdinaryPurgePreparationV2) joinCohortStopV3(ctx context.Context, operation *SessionOperation) (*StoppedOrdinarySessionV1, error) {
	if ctx == nil || preparation.closed || preparation.transferred || preparation.restored || preparation.owner == nil || preparation.owner.operation != operation || !preparation.decisionReady || preparation.decision == nil || preparation.decision.operation != operation || preparation.decision.owner == nil {
		return nil, sessionLeaseError("the ordinary cohort stop lost its SAME original ready monitor")
	}
	first, err := preparation.decision.owner.FirstDecisionV3(ctx)
	if err != nil || first != PurgeCohortCommitByteV3 {
		return nil, errors.Join(sessionLeaseError("the original ordinary monitor has no globally selected native commit"), err)
	}
	preparation.owner.stopSent = true
	if err := preparation.owner.settleOrdinaryV1(); err != nil {
		return nil, err
	}
	stopped := &StoppedOrdinarySessionV1{ordinaryHolderCaptureV2: preparation.owner}
	if err := stopped.Check(context.Background(), operation); err != nil {
		return nil, err
	}
	if err := preparation.decision.Close(); err != nil {
		return nil, err
	}
	preparation.transferred = true
	return stopped, nil
}
