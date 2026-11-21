package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureRewind) }

func (b *bridge) handleCheckpointV1(req proto.Request) bool {
	if req.Op != api.OpCheckpoints && req.Op != api.OpRewind {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	if req.Op == api.OpRewind {
		b.reply(req.ID, nil, UserError("rewind_preparation", "rewind needs its original closed preparation and fresh delivery grants", "Use the current client to prepare and commit rewind on the same verified connection."))
		return true
	}
	var selected string
	if req.Op == api.OpCheckpoints {
		var args api.CheckpointsArgs
		if json.Unmarshal(req.Args, &args) != nil || args.Validate() != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the checkpoint selection is invalid", "Use the complete session address."))
			return true
		}
		selected = args.ID
	} else {
		var args api.RewindArgs
		if json.Unmarshal(req.Args, &args) != nil || args.Validate() != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the rewind selection is invalid", "List the checkpoints, then select one of their numbers."))
			return true
		}
		selected = args.ID
	}
	primary, err := SandboxPrimaryID(selected)
	if err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	result, err := HolderRequestWithFeatures(primary, req.Op, json.RawMessage(req.Args), 30*time.Second, api.FeatureRewind)
	b.reply(req.ID, json.RawMessage(result), err)
	return true
}
