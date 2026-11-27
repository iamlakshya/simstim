package supervisor

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) memberRequest(c *client, req proto.Request) bool {
	switch req.Op {
	case "spawn":
		var a api.JoinArgs
		if json.Unmarshal(req.Args, &a) != nil {
			c.reply(req.ID, nil, execRefusal("invalid spawn request"))
			return true
		}
		row, err := s.spawnContextV1(c.requestContextV1(s.owner().lifetimeContext()), a)
		if err != nil {
			c.reply(req.ID, nil, execRefusal(err.Error()))
		} else {
			c.reply(req.ID, row, nil)
		}
	case "member.rm":
		err := s.owner().stopOnlyMember(s.id)
		if err != nil {
			c.reply(req.ID, nil, execRefusal(err.Error()))
		} else {
			c.reply(req.ID, struct{}{}, nil)
		}
	default:
		return false
	}
	return true
}
