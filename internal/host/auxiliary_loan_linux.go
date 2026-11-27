//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const auxiliaryReadyOperationV1 = "operation_ready_v1"

// The two descriptors are duplicated from the SAME original owning OFDs.
// The actual newly created helper's cap, cgroup, executable and creation
// lifetime are rechecked inside the existing publication gate before send.
func (owner *auxiliaryOperationV1) sendAuxiliaryLoanV1(ctx context.Context) (returned error) {
	if owner == nil || owner.line == nil || owner.activation == nil || owner.loanSent {
		return auxiliaryUnavailableV1("the auxiliary native loan has no original unused activation")
	}
	if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
		return err
	}
	if owner.activation.peer.identity == nil {
		return auxiliaryUnavailableV1("the auxiliary native code identity did not arrive before its private loan")
	}
	source, err := owner.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	runtimeBytes, err := startupRuntimeBytes(source)
	if err != nil {
		return err
	}
	loan := auxiliaryLoanV1{Version: 1, Primary: owner.operation.Primary(), Nonce: owner.hello.StartupNonce, Token: owner.reservation.Token, Need: startupDigest(source.VMNeedV1), Runtime: startupDigest(runtimeBytes)}
	operation, err := owner.operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	holder, err := duplicateAuxiliaryHolderV1(ctx, owner.vacancy.holder)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, holder.Close()) }()
	args, err := json.Marshal(loan)
	if err != nil {
		return err
	}
	if err := owner.publication.gate(owner.ctx, func() error {
		if err := requireAuxiliaryAccountingOriginV1(owner.ctx, owner.operation); err != nil {
			return err
		}
		if err := owner.checkAuxiliaryNativeMemoryV1(ctx); err != nil {
			return err
		}
		owner.loanSent = true // A partial native send must be settled as a loan.
		return owner.line.writeAuxiliaryLoanV1(proto.Request{ID: 1, Op: auxiliaryLoanOperationV1, Args: args}, operation, holder, owner.artifacts.disk)
	}); err != nil {
		return err
	}
	result, err := owner.auxiliaryResponseV1(ctx, 1)
	if err != nil {
		return err
	}
	var ack struct {
		Nonce string `json:"operation_nonce"`
	}
	if !api.ExactJSONFields(result, ack) || json.Unmarshal(result, &ack) != nil || ack.Nonce != loan.Nonce {
		return auxiliaryUnavailableV1("the new native auxiliary helper did not acknowledge the original OFD pair")
	}
	if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: 2, Op: auxiliaryReadyOperationV1}); err != nil {
		return err
	}
	result, err = owner.auxiliaryResponseV1(ctx, 2)
	if err != nil {
		return err
	}
	if !api.ExactJSONFields(result, ack) || json.Unmarshal(result, &ack) != nil || ack.Nonce != loan.Nonce {
		return auxiliaryUnavailableV1("the actual auxiliary guest has no matching prepared continuation")
	}
	return owner.checkAuxiliaryNativeMemoryV1(ctx)
}
func (owner *auxiliaryOperationV1) auxiliaryResponseV1(ctx context.Context, id uint64) (json.RawMessage, error) {
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = owner.line.SetDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = owner.line.SetDeadline(deadline)
	} else {
		_ = owner.line.SetDeadline(time.Time{})
	}
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return nil, err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return nil, err
	}
	sender, err := owner.line.currentSender()
	if err != nil {
		return nil, err
	}
	if err := owner.activation.peer.checkNativeSender(sender); err != nil {
		return nil, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil || response.ID != id {
		return nil, auxiliaryUnavailableV1("the auxiliary reply differs from its original request")
	}
	if !response.OK {
		if response.Error != nil {
			return nil, response.Error
		}
		return nil, auxiliaryUnavailableV1("the actual auxiliary helper refused its operation")
	}
	return response.Result, ctx.Err()
}
