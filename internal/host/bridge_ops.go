package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// moreFeatures are the features that set hosts and sessions up: the requests
// in this file, a new session's secrets (secrets.go), its missing folder
// (folders.go), and trust in a repository's setup script (setup.go).
var moreFeatures = []string{"agents.check", "doctor.fix", "secrets", "mkdir", "setup.trust"}

func init() { Features = append(Features, moreFeatures...) }

// handleMore serves the requests that set a host up. It reports whether it
// knew the request.
func (b *bridge) handleMore(req proto.Request) bool {
	switch req.Op {
	case "agents.check":
		b.reply(req.ID, AgentChecks(), nil)
	case "doctor.fix":
		var a api.FixArgs
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &a); err != nil {
				b.reply(req.ID, nil, UserError("bad_request", "the fix request is not valid JSON", "Update the client."))
				return true
			}
		}
		b.reply(req.ID, Fixes(a), nil)
	default:
		return false
	}
	return true
}
