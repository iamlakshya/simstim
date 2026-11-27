package supervisor

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, api.FeatureTrace, api.FeatureDebug) }

func (s *Supervisor) debugTraceRequestV1(c *client, req proto.Request, frame proto.Frame) (bool, bool) {
	switch req.Op {
	case api.OpDebugTraceReadV1:
		var args api.DebugTraceReadArgsV1
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.ID != s.id || args.Offset < 0 || len(args.Epoch) > 64 {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "the trace read does not name this original holder", Hint: "Collect the current session's trace through its full address."})
			return true, true
		}
		return true, s.pass(c, frame)
	case api.OpDebugStart, api.OpDebugStop:
		var args api.DebugStartArgs
		if !api.ExactJSONFields(req.Args, args) || json.Unmarshal(req.Args, &args) != nil || args.ID != s.id || len(args.Categories) > 16 || args.Seconds < 0 || args.Seconds > 86400 {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "the debug request does not name this original holder", Hint: "Use the current session's full address."})
			return true, true
		}
		return true, s.pass(c, frame)
	}
	return false, false
}
