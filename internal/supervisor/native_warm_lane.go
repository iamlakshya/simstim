package supervisor

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// The ordinary buffered reader is promoted only at the empty boundary of
// the explicit native resume inspection. Every following native role is
// claimed from its actual frame before the next physical read.
func (s *Supervisor) nativeWarmLaneFrameV1(client *client, frame proto.Frame) bool {
	scope := client.nativeScopeV1()
	if scope == nil || scope.purpose != proto.NativeResumeInspectV1 || scope.line == nil {
		return false
	}
	if frame.Type == proto.THello {
		var hello proto.Hello
		if !api.ExactJSONFields(frame.Payload, hello) || json.Unmarshal(frame.Payload, &hello) != nil || hello.Check(version.Protocol) != nil || hello.Role != proto.NativeWarmRoleV1 || hello.StartupNonce != scope.state.Nonce {
			_ = scope.line.RejectAncillary()
			client.close()
			return true
		}
		if err := scope.checkNativeRuntimeV1(scope.lifetime); err != nil {
			_ = scope.line.RejectAncillary()
			client.reply(7, nil, toProtoErr(err))
			return true
		}
		if err := scope.line.AcceptNativeHelloV1(frame); err != nil {
			client.reply(7, nil, toProtoErr(err))
			return true
		}
		client.reply(7, scope.state, nil)
		return true
	}
	if frame.Type != proto.TRequest {
		_ = scope.line.RejectAncillary()
		client.close()
		return true
	}
	if err := scope.owner.CheckWarmSenderV1(scope.line); err != nil {
		_ = scope.line.RejectAncillary()
		client.close()
		return true
	}
	var request proto.Request
	if !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil {
		_ = scope.line.RejectAncillary()
		client.close()
		return true
	}
	switch request.Op {
	case proto.NativeWarmLoanV1:
		scope.mu.Lock()
		exists := scope.operation != nil || scope.released
		scope.mu.Unlock()
		if exists {
			_ = scope.line.RejectAncillary()
			client.reply(request.ID, nil, toProtoErr(execRefusal("this native warm lane already borrowed its incoming operation")))
			return true
		}
		operation, err := scope.owner.AcceptWarmOperationV1(scope.lifetime, scope.line, frame, scope.state)
		if err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		scope.mu.Lock()
		scope.operation = operation
		scope.mu.Unlock()
		if err := scope.checkCurrentV1(scope.lifetime); err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		client.reply(request.ID, scope.state, nil)
		return true
	case proto.NativeWarmCancelV1, proto.NativeWarmReleaseV1:
		if err := scope.line.RejectAncillary(); err != nil {
			client.close()
			return true
		}
		var args proto.NativeStopArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != scope.state.Primary || args.Nonce != scope.state.Nonce {
			client.reply(request.ID, nil, toProtoErr(execRefusal("the warm cleanup has another original scope")))
			return true
		}
		if err := scope.joinIncomingWarmWorkerV1(); err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return true
		}
		if request.Op == proto.NativeWarmReleaseV1 {
			if err := scope.closeIncomingWarmOperationV1(); err != nil {
				client.reply(request.ID, nil, toProtoErr(err))
				return true
			}
		}
		client.reply(request.ID, scope.state, nil)
		return true
	case proto.NativeResumeCheckV1, proto.NativeWarmResumeV1:
		return false
	default:
		if s.nativeNoneStopRequestV1(client, scope, request) {
			return true
		}
		if s.nativeCheckpointReservationLaneV2(client, scope, request) {
			return true
		}
		if s.nativeRewindStopLaneV1(client, scope, request) {
			return true
		}
		if s.nativeCheckpointLaneV1(client, scope, request) {
			return true
		}
		if s.nativeForkLaneV1(client, scope, frame, request) {
			return true
		}
		_ = scope.line.RejectAncillary()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the native warm lane accepts only its original closed operations")))
		client.close()
		return true
	}
}
func (scope *nativeStopScopeV1) joinIncomingWarmWorkerV1() error {
	scope.joinCheckpointReservationV2()
	scope.joinCheckpointLoanV1()
	if err := scope.joinForkJobV1(); err != nil {
		return err
	}
	scope.mu.Lock()
	cancel, done := scope.warmCancel, scope.warmDone
	scope.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.warmUncertain
}
func (scope *nativeStopScopeV1) closeIncomingWarmOperationV1() error {
	if scope == nil {
		return nil
	}
	scope.mu.Lock()
	noneStop := scope.noneStop
	scope.mu.Unlock()
	if noneStop != nil {
		return noneStop.closeBorrowV1()
	}
	if err := scope.joinIncomingWarmWorkerV1(); err != nil {
		return err
	}
	scope.mu.Lock()
	operation := scope.operation
	scope.mu.Unlock()
	if operation != nil {
		if err := operation.CloseWithError(); err != nil {
			return err
		}
	}
	scope.mu.Lock()
	scope.released = true
	if scope.operation == operation {
		scope.operation = nil
	}
	scope.mu.Unlock()
	return nil
}
func (client *client) nativeScopeV1() *nativeStopScopeV1 {
	client.nativeMu.Lock()
	defer client.nativeMu.Unlock()
	return client.nativeStop
}

func (client *client) setNativeScopeV1(scope *nativeStopScopeV1) {
	client.nativeMu.Lock()
	client.nativeStop = scope
	client.nativeMu.Unlock()
}

// markWarmMutationV1 only narrows cleanup after the actual backend starts IO.
// It cannot create a scope, native borrow or permission from status data.
func (scope *nativeStopScopeV1) markWarmMutationV1() {
	scope.mu.Lock()
	scope.warmMutation = true
	scope.mu.Unlock()
}
func (scope *nativeStopScopeV1) retainUncertainWarmBorrowV1(err error) {
	scope.mu.Lock()
	scope.warmUncertain = err
	scope.mu.Unlock()
	select {}
}
