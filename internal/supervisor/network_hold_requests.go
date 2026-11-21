package supervisor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func endedNetworkRequest() *proto.Error {
	return &proto.Error{Code: "network_request_ended", Message: "pending network request has ended or its actor is unavailable", Hint: "Refresh this session's pending requests, then retry."}
}

func (s *Supervisor) liveNetworkActor() *egressProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eg == nil || s.eg.dead.Load() || s.eg.ctl == nil || channelClosed(s.eg.ctl.done) {
		return nil
	}
	return s.eg
}

func (s *Supervisor) sameNetworkActor(actor *egressProc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return actor != nil && s.eg == actor && !actor.dead.Load() && actor.ctl != nil && !channelClosed(actor.ctl.done)
}

func (s *Supervisor) readNetworkHolds(ctx context.Context) (api.NetworkHoldSnapshot, *proto.Error) {
	actor := s.liveNetworkActor()
	if actor == nil {
		return api.NetworkHoldSnapshot{}, endedNetworkRequest()
	}
	var snapshot api.NetworkHoldSnapshot
	if err := actor.ctl.request(ctx, egress.CtlNetworkPending, struct{}{}, &snapshot); err != nil {
		return api.NetworkHoldSnapshot{}, toProtoErr(err)
	}
	if !s.sameNetworkActor(actor) {
		return api.NetworkHoldSnapshot{}, endedNetworkRequest()
	}
	if !api.ValidNetworkHoldSnapshot(snapshot) {
		return api.NetworkHoldSnapshot{}, &proto.Error{Code: "network_snapshot", Message: "pending network snapshot is invalid or unsupported", Hint: "Update matching helpers before using pending requests."}
	}
	return snapshot, nil
}

// The receiver is the originally requested member, before owner forwarding.
// Its original session ID is checked before the SAME root actor is selected.
func (s *Supervisor) networkHoldRequest(c *client, request proto.Request) bool {
	if request.Op != api.OpPolicyPending && request.Op != api.OpPolicyRefuse && request.Op != api.OpPolicyDescribe && request.Op != api.OpPolicyResolve && request.Op != api.OpPolicyAnswer {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := s.owner()
	switch request.Op {
	case api.OpPolicyPending:
		var args api.NetworkHoldQueryArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldQueryArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 || args.Session != s.id || !api.ValidSessionID(args.Session) {
			c.reply(request.ID, nil, &proto.Error{Code: "bad_request", Message: "pending network query has invalid original session or version", Hint: "Refresh the selected session with a matching client."})
			return true
		}
		snapshot, failure := root.readNetworkHolds(ctx)
		c.reply(request.ID, snapshot, failure)
	case api.OpPolicyDescribe:
		var args api.NetworkHoldDescribeArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldDescribeArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 || args.Session != s.id || !api.ValidSessionID(args.Session) || !api.ValidNetworkHoldID(args.Request) {
			c.reply(request.ID, nil, &proto.Error{Code: "bad_request", Message: "pending descriptor has invalid original session, ID or version", Hint: "Refresh this original session and request with matching helpers."})
			return true
		}
		actor := root.liveNetworkActor()
		if actor == nil {
			c.reply(request.ID, nil, endedNetworkRequest())
			return true
		}
		var descriptor api.NetworkHoldDescriptor
		failure := actor.ctl.request(ctx, egress.CtlNetworkDescribe, struct {
			Version int    `json:"v"`
			ID      string `json:"id"`
		}{api.NetworkHoldV1, args.Request}, &descriptor)
		if !root.sameNetworkActor(actor) {
			c.reply(request.ID, nil, endedNetworkRequest())
			return true
		}
		if failure != nil {
			c.reply(request.ID, nil, toProtoErr(failure))
			return true
		}
		if !api.ValidNetworkHoldDescriptor(descriptor, args.Request) || descriptor.Request != args.Request {
			c.reply(request.ID, nil, &proto.Error{Code: "network_snapshot", Message: "pending descriptor is invalid or inconsistent", Hint: "Refresh this request with matching helpers."})
			return true
		}
		c.reply(request.ID, descriptor, nil)

	case api.OpPolicyResolve, api.OpPolicyAnswer:
		var args api.NetworkHoldActionArgs
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || !api.ValidNetworkHoldPositiveAction(args) || args.Session != s.id || request.Op == api.OpPolicyResolve && args.Scope == api.NetworkHoldOnce || request.Op == api.OpPolicyAnswer && args.Scope != api.NetworkHoldOnce && args.Scope != api.NetworkHoldSession {
			c.reply(request.ID, nil, &proto.Error{Code: "bad_request", Message: "pending resolution has no exact original session, request or scope", Hint: "Refresh the original request and choose one of its available answer scopes."})
			return true
		}
		if root.member(s.id) != s {
			c.reply(request.ID, nil, endedNetworkRequest())
			return true
		}
		if request.Op == api.OpPolicyAnswer {
			value, failure := root.answerNetworkHoldV1(ctx, args)
			c.reply(request.ID, value, failure)
		} else {
			value, failure := root.resolveNetworkHoldV1(ctx, args)
			c.reply(request.ID, value, failure)
		}

	case api.OpPolicyRefuse:
		var args api.NetworkHoldActionArgs
		if !api.ExactJSONFields(request.Args, api.NetworkHoldActionArgs{}) || json.Unmarshal(request.Args, &args) != nil || args.Version != api.NetworkHoldV1 || args.Session != s.id || !api.ValidSessionID(args.Session) || !api.ValidNetworkHoldID(args.Request) || args.Scope != "" || args.Revision != 0 || args.Maximum != "" {
			c.reply(request.ID, nil, &proto.Error{Code: "bad_request", Message: "pending refusal has invalid original session, ID or version", Hint: "Refresh the selected request and retry without an answer scope."})
			return true
		}
		actor := root.liveNetworkActor()
		if actor == nil {
			c.reply(request.ID, nil, endedNetworkRequest())
			return true
		}
		failure := actor.ctl.request(ctx, egress.CtlNetworkRefuse, struct {
			ID string `json:"id"`
		}{ID: args.Request}, nil)
		if !root.sameNetworkActor(actor) {
			c.reply(request.ID, nil, endedNetworkRequest())
			return true
		}
		if failure != nil {
			c.reply(request.ID, nil, toProtoErr(failure))
			return true
		}
		c.reply(request.ID, struct{}{}, nil)
	}
	return true
}
