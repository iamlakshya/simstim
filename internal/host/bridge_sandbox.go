package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/proto"
)

// sandboxFeatures are the bridge features for sandboxes (isolation spec 10.5).
var sandboxFeatures = []string{"sandbox"}

func init() { Features = append(Features, sandboxFeatures...) }

// handleSandbox serves sandbox bridge requests. It reports whether it knew
// the request.
func (b *bridge) handleSandbox(req proto.Request) bool {
	switch req.Op {
	case "sandbox.probe":
		var a struct {
			ReleaseURL string `json:"release_url"`
		}
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &a); err != nil {
				b.reply(req.ID, nil, UserError("bad_request", "the sandbox.probe request is not valid JSON", "Update the client."))
				return true
			}
		}
		b.reply(req.ID, CapabilityProbe(a.ReleaseURL), nil)
	default:
		return false
	}
	return true
}
