package supervisor

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A closed rewind consumes the SAME already-accepted live native lane. This
// acknowledgement is intent only; the outside original creator must still
// join its retained pidfd and the entire real unit/cgroup before storage IO.
func (s *Supervisor) nativeRewindStopLaneV1(client *client, scope *nativeStopScopeV1, request proto.Request) bool {
	if request.Op != proto.NativeRewindStopV1 {
		return false
	}
	if err := scope.line.RejectAncillary(); err != nil {
		client.close()
		return true
	}
	var args proto.NativeStopArgsV1
	if request.ID != proto.NativeRewindStopRequestV1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || args.Primary != scope.state.Primary || args.Nonce != scope.state.Nonce {
		client.reply(request.ID, nil, toProtoErr(execRefusal("the rewind stop changed its original native scope")))
		return true
	}
	if err := scope.joinIncomingWarmWorkerV1(); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	if err := scope.checkCurrentV1(scope.lifetime); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	scope.mu.Lock()
	if scope.used || scope.released || scope.operation == nil {
		scope.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the original rewind stop was already consumed")))
		return true
	}
	scope.mu.Unlock()
	root := s.owner()
	if err := root.retainManagerCustodyForSameRequestStopV1(scope.lifetime, scope.owner); err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	scope.mu.Lock()
	if scope.used || scope.released || scope.operation == nil {
		scope.mu.Unlock()
		client.reply(request.ID, nil, toProtoErr(execRefusal("the original rewind stop ended while native custody was retained")))
		return true
	}
	scope.used = true
	scope.mu.Unlock()
	root.markStopIntent()
	raw, err := json.Marshal(scope.state)
	if err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	encoded, err := json.Marshal(proto.Response{ID: request.ID, OK: true, Result: raw})
	if err != nil {
		client.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	if client.write(proto.TResponse, encoded) != nil {
		return true
	}
	select {
	case root.stop <- struct{}{}:
	default:
	}
	return true
}
