package supervisor

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeatureNativeCheckpointCustodyV1) }

func (s *Supervisor) nativeCheckpointLaneV1(client *client, scope *nativeStopScopeV1, request proto.Request) bool {
	if request.Op != proto.NativeCheckpointCustodyV1 {
		return false
	}
	if err := scope.line.RejectAncillary(); err != nil {
		client.close()
		return true
	}
	var args proto.NativeStopArgsV1
	if request.ID != proto.NativeCheckpointCustodyRequestV1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != scope.state.Primary || args.Nonce != scope.state.Nonce {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the Snapshot transfer has another original native scope")))
		return true
	}
	if err := scope.checkCurrentV1(scope.lifetime); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	scope.mu.Lock()
	if scope.snapshotStarted || scope.released || scope.operation == nil {
		scope.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the original Snapshot loan was already issued or ended")))
		return true
	}
	ctx, cancel := context.WithCancel(scope.lifetime)
	scope.snapshotStarted = true
	scope.snapshotCancel = cancel
	scope.snapshotDone = make(chan struct{})
	operation := scope.operation
	scope.mu.Unlock()
	go func() {
		defer close(scope.snapshotDone)
		defer cancel()
		actual, ok := scope.runtime.(vmRuntime)
		if !ok || actual.Sandbox == nil || scope.native == nil || scope.connection == nil {
			client.reply(request.ID, nil, toProtoErr(execRefusal("the original Snapshot source is not a native microvm")))
			return
		}
		own, err := scope.native.OriginalAccountModeV1(ctx, actual.Sandbox, scope.connection)
		if err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return
		}
		mode := "local"
		if own {
			mode = "slot"
		}
		result := proto.NativeCheckpointCustodyResultV1{State: scope.state, Mode: mode}
		if !own {
			client.reply(request.ID, result, nil)
			return
		}
		raw, err := json.Marshal(result)
		if err != nil {
			client.reply(request.ID, nil, toProtoErr(err))
			return
		}
		attempted, err := scope.native.SendCheckpointLoanV1(ctx, scope.connection, scope.owner, operation, scope.line, proto.Response{ID: request.ID, OK: true, Result: raw})
		if err != nil {
			if attempted {
				scope.mu.Lock()
				scope.warmUncertain = err
				scope.mu.Unlock()
				go client.close()
				return
			}
			client.reply(request.ID, nil, toProtoErr(err))
		}
	}()
	return true
}

func (scope *nativeStopScopeV1) joinCheckpointLoanV1() {
	scope.mu.Lock()
	cancel, done := scope.snapshotCancel, scope.snapshotDone
	scope.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
