package supervisor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) modelUsageRequest(c *client, request proto.Request) bool {
	if request.Op != egress.CtlModelUsage {
		return false
	}
	if !c.full {
		c.reply(request.ID, nil, host.ErrInside)
		return true
	}
	if len(request.Args) > 0 && (!api.ExactJSONFields(request.Args, struct{}{}) || json.Unmarshal(request.Args, &struct{}{}) != nil) {
		c.reply(request.ID, nil, toProtoErr(secretIntentFailure()))
		return true
	}
	root := s.owner()
	actor := root.liveNetworkActor()
	if actor == nil {
		c.reply(request.ID, nil, endedNetworkRequest())
		return true
	}
	ctx, cancel := context.WithTimeout(root.lifetimeContext(), 500*time.Millisecond)
	defer cancel()
	var usage api.ModelUsageV1
	err := actor.ctl.request(ctx, egress.CtlModelUsage, struct{}{}, &usage)
	if err != nil {
		c.reply(request.ID, nil, toProtoErr(err))
		return true
	}
	if !root.sameNetworkActor(actor) || !api.ValidModelUsageV1(usage) {
		c.reply(request.ID, nil, endedNetworkRequest())
		return true
	}
	c.reply(request.ID, usage, nil)
	return true
}
