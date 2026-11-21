package host

import (
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureFork) }

func (b *bridge) handleForkV1(request proto.Request) bool {
	if request.Op != api.OpFork {
		return false
	}
	var args api.ForkArgs
	if request.ID == 0 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Validate() != nil {
		b.reply(request.ID, nil, forkFailureV1("bad_request", "fork needs one complete session address and a count from 1 to 32"))
		return true
	}
	result, err := forkContextV1(b.ctx, args)
	b.reply(request.ID, result, err)
	return true
}

func forkResultErrorV1(err error) proto.Error {
	var user *proto.Error
	if errors.As(err, &user) && user != nil {
		owned := *user
		owned.Data = nil
		owned.Message = api.CleanText(owned.Message, 1000)
		owned.Hint = api.CleanText(owned.Hint, 1000)
		return owned
	}
	return *UserError("fork_child", "the child could not start: "+err.Error(), "Inspect the source sandbox and host doctor, then retry fork.")
}
