package supervisor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

// answerNetworkHoldV1 receives only the same actor's opaque protected-memory
// grant result. A non-sensitive session snapshot goes through the separate
// actual durable writer and returns no target on this answer channel.
func (s *Supervisor) answerNetworkHoldV1(ctx context.Context, args api.NetworkHoldActionArgs) (api.NetworkHoldAnswerResult, *proto.Error) {
	var empty api.NetworkHoldAnswerResult
	s.polMu.Lock()
	actor := s.liveNetworkActor()
	if actor == nil {
		s.polMu.Unlock()
		return empty, endedNetworkRequest()
	}
	var raw json.RawMessage
	err := actor.ctl.request(ctx, egress.CtlNetworkAnswer, args, &raw)
	same := s.sameNetworkActor(actor)
	s.polMu.Unlock()
	if !same {
		return empty, endedNetworkRequest()
	}
	if err != nil {
		var refusal *proto.Error
		if args.Scope == api.NetworkHoldSession && errors.As(err, &refusal) && refusal.Code == "network_publication_required" {
			if _, failure := s.resolveNetworkHoldFromActorV1(ctx, args, actor); failure != nil {
				return empty, failure
			}
			return api.NetworkHoldAnswerResult{Version: api.NetworkHoldV1, Scope: api.NetworkHoldSession}, nil
		}
		return empty, toProtoErr(err)
	}
	var result api.NetworkHoldAnswerResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != api.NetworkHoldV1 || result.Scope != args.Scope || !result.MemoryOnly || len(result.RuleIDs) != 1 || !api.ValidNetworkHoldID(result.RuleIDs[0]) {
		return empty, invalidNetworkResolutionV1()
	}
	return result, nil
}
