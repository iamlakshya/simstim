//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func (prepared *WarmResumeV1) readNativeReplyV1() (proto.Response, error) {
	owner := prepared.owner
	frame, err := owner.line.ReadFrame()
	if err != nil {
		return proto.Response{}, err
	}
	if err := owner.line.RejectAncillary(); err != nil {
		return proto.Response{}, err
	}
	if err := owner.checkCurrentStopSenderV1(context.Background()); err != nil {
		return proto.Response{}, err
	}
	var response proto.Response
	if frame.Type != proto.TResponse || !api.ExactJSONFields(frame.Payload, response) || json.Unmarshal(frame.Payload, &response) != nil {
		return response, auxiliaryUnavailableV1("the original native warm reply changed its frame")
	}
	return response, nil
}
func (prepared *WarmResumeV1) readNativeStateV1(id uint64) error {
	response, err := prepared.readNativeReplyV1()
	if err != nil {
		return err
	}
	if response.ID != id || !response.OK {
		if response.Error != nil {
			return response.Error
		}
		return auxiliaryUnavailableV1("the original native warm control refused its scope")
	}
	var state proto.NativeStopStateV1
	if !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != prepared.owner.state {
		return auxiliaryUnavailableV1("the original native warm control changed its retained source")
	}
	return nil
}
func (prepared *WarmResumeV1) lendIncomingOperationV1(ctx context.Context, operation *SessionOperation) (returned error) {
	owner := prepared.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := owner.line.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if err := owner.line.WriteNativeHelloV1(proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: proto.NativeWarmRoleV1, StartupNonce: owner.state.Nonce}); err != nil {
		return err
	}
	if err := prepared.readNativeStateV1(7); err != nil {
		return err
	}
	file, err := operation.native.duplicateLockedFile(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	raw, err := json.Marshal(proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce})
	if err != nil {
		return err
	}
	prepared.loanSent = true // uncertain delivery retains the SAME source.
	if err := owner.line.writeLoan(proto.Request{ID: 8, Op: proto.NativeWarmLoanV1, Args: raw}, file); err != nil {
		return err
	}
	return prepared.readNativeStateV1(8)
}
func (prepared *WarmResumeV1) writeWarmControlV1(id uint64, op string) error {
	owner := prepared.owner
	raw, err := json.Marshal(proto.NativeStopArgsV1{Version: 1, Primary: owner.operation.Primary(), Nonce: owner.state.Nonce})
	if err != nil {
		return err
	}
	return owner.line.WriteFrame(proto.TRequest, proto.Request{ID: id, Op: op, Args: raw})
}
func (prepared *WarmResumeV1) releaseIncomingOperationV1() error {
	if err := prepared.owner.line.SetDeadline(time.Time{}); err != nil {
		return err
	}
	if err := prepared.writeWarmControlV1(6, proto.NativeWarmReleaseV1); err != nil {
		return err
	}
	for {
		response, err := prepared.readNativeReplyV1()
		if err != nil {
			return err
		}
		if response.ID == 3 || response.ID == 5 || response.ID == 8 || response.ID == proto.NativeForkDestinationRequestV1 || response.ID == proto.NativeForkCopyRequestV1 || response.ID == proto.NativeCheckpointCustodyRequestV1 || response.ID == proto.NativeCheckpointReservationRequestV2 {
			continue
		}
		if response.ID != 6 || !response.OK {
			if response.Error != nil {
				return response.Error
			}
			return auxiliaryUnavailableV1("the native warm cleanup has no explicit joined release")
		}
		var state proto.NativeStopStateV1
		if !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != prepared.owner.state {
			return auxiliaryUnavailableV1("the native warm release changed its original scope")
		}
		prepared.loanSent = false
		return nil
	}
}
func (prepared *WarmResumeV1) retainUnknownWarmV1(failure error) error {
	owner := prepared.owner
	// Closing the connection asks the same actual worker to cancel and join.
	// EOF is not its acknowledgement: retain all original rights until actual
	// independent whole-unit closure if its explicit release reply was lost.
	_ = owner.line.Close()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := owner.checkStoppedNativeFactsV1(ctx)
		cancel()
		if err == nil {
			return errors.Join(failure, owner.Close())
		}
		if owner.manager.checkNativeManager() != nil {
			return owner.retainUnknownStopV1(errors.Join(failure, err))
		}
		time.Sleep(25 * time.Millisecond)
	}
}
func (prepared *WarmResumeV1) resumeWithIncomingOperationV1(ctx context.Context, operation *SessionOperation, args api.ResumeArgs) (*api.ResumeResult, error) {
	if err := prepared.Check(ctx, operation); err != nil {
		return nil, err
	}
	if err := checkResumeExpectedImageV1(ctx, operation, args.ExpectedImageV1); err != nil {
		return nil, err
	}
	request, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	defer clear(request)
	fields, err := api.JSONObjectFields(request)
	if err != nil {
		return nil, err
	}
	if keys, exists := fields["named_ssh_keys"]; exists && !bytes.Equal(keys, []byte("null")) {
		return nil, auxiliaryUnavailableV1("a warm controller cannot receive a new named-key payload")
	}
	owner := prepared.owner
	owner.mu.Lock()
	if owner.closed || prepared.used || !prepared.loanSent {
		owner.mu.Unlock()
		return nil, auxiliaryUnavailableV1("the original native warm loan was consumed or ended")
	}
	prepared.used = true
	if err := ctx.Err(); err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	if err := owner.line.SetDeadline(time.Time{}); err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	raw, err := json.Marshal(proto.NativeWarmResumeArgsV1{Version: 1, Scope: proto.NativeStopArgsV1{Version: 1, Primary: operation.Primary(), Nonce: owner.state.Nonce}, Request: request})
	if err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	defer clear(raw)
	if err := owner.line.WriteFrame(proto.TRequest, proto.Request{ID: 3, Op: proto.NativeWarmResumeV1, Args: raw}); err != nil {
		owner.mu.Unlock()
		return nil, err
	}
	cancelWritten := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { cancelWritten <- prepared.writeWarmControlV1(5, proto.NativeWarmCancelV1) })
	var reply *proto.Response
	cancelSent, cancelJoined := false, false
	for {
		response, readErr := prepared.readNativeReplyV1()
		if readErr != nil {
			if !stop() {
				<-cancelWritten
			}
			owner.mu.Unlock()
			return nil, readErr
		}
		switch response.ID {
		case 3:
			owned := response
			reply = &owned
		case 5:
			var state proto.NativeStopStateV1
			if !response.OK || !api.ExactJSONFields(response.Result, state) || json.Unmarshal(response.Result, &state) != nil || state != owner.state {
				if !stop() {
					<-cancelWritten
				}
				owner.mu.Unlock()
				return nil, auxiliaryUnavailableV1("native warm cancellation has no actual joined acknowledgement")
			}
			cancelJoined = true
		default:
			if !stop() {
				<-cancelWritten
			}
			owner.mu.Unlock()
			return nil, auxiliaryUnavailableV1("the native warm worker replied outside its original request")
		}
		if reply != nil && !cancelSent {
			if stop() {
				break
			}
			if err := <-cancelWritten; err != nil {
				owner.mu.Unlock()
				return nil, err
			}
			cancelSent = true
		}
		if reply != nil && cancelSent && cancelJoined {
			break
		}
	}
	owner.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !reply.OK {
		if reply.Error != nil {
			return nil, reply.Error
		}
		return nil, auxiliaryUnavailableV1("the native warm worker refused its original agent resume")
	}
	var result api.ResumeResult
	if !api.ExactJSONFields(reply.Result, result) || json.Unmarshal(reply.Result, &result) != nil {
		return nil, auxiliaryUnavailableV1("the native warm worker returned an invalid result")
	}
	if err := prepared.Check(ctx, operation); err != nil {
		return nil, err
	}
	return &result, nil
}
