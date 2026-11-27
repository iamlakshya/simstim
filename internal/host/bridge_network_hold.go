package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This bridge resolves only the original requested live supervisor. It
// neither reads an offline replacement nor reconstructs a target from display.
func (b *bridge) handleNetworkHold(request proto.Request) bool {
	if request.Op != api.OpPolicyPending && request.Op != api.OpPolicyRefuse && request.Op != api.OpPolicyDescribe && request.Op != api.OpPolicyResolve && request.Op != api.OpPolicyAnswer {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(request.ID, nil, err)
		return true
	}
	var session string
	if request.Op == api.OpPolicyPending {
		var args api.NetworkHoldQueryArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldQueryArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 {
			b.reply(request.ID, nil, UserError("bad_request", "invalid pending network query", "Use a matching client and the selected original session."))
			return true
		}
		session = args.Session
	} else if request.Op == api.OpPolicyDescribe {
		var args api.NetworkHoldDescribeArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldDescribeArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 || !api.ValidNetworkHoldID(args.Request) {
			b.reply(request.ID, nil, UserError("bad_request", "invalid pending network descriptor query", "Use the original session and opaque request ID with matching helpers."))
			return true
		}
		session = args.Session
	} else if request.Op == api.OpPolicyResolve || request.Op == api.OpPolicyAnswer {
		var args api.NetworkHoldActionArgs
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || !api.ValidNetworkHoldPositiveAction(args) || request.Op == api.OpPolicyResolve && args.Scope == api.NetworkHoldOnce || request.Op == api.OpPolicyAnswer && args.Scope != api.NetworkHoldOnce && args.Scope != api.NetworkHoldSession {
			b.reply(request.ID, nil, UserError("bad_request", "invalid original pending network resolution", "Refresh the original request and choose one of its available answer scopes."))
			return true
		}
		session = args.Session
	} else {
		var args api.NetworkHoldActionArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldActionArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 || !api.ValidNetworkHoldID(args.Request) || args.Scope != "" || args.Revision != 0 || args.Maximum != "" {
			b.reply(request.ID, nil, UserError("bad_request", "invalid pending network refusal", "Refresh the selected request and retry without an answer scope."))
			return true
		}
		session = args.Session
	}
	if !validID(session) || !alive(session) {
		b.reply(request.ID, nil, UserError("network_request_ended", "pending request's original session is not running", "Refresh the selected session before answering a pending request."))
		return true
	}
	result, err := HolderRequestWithFeatures(session, request.Op, request.Args, 5*time.Second, api.FeatureNetworkHolds)
	b.reply(request.ID, result, err)
	return true
}
