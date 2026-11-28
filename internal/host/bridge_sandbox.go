package host

import (
	"encoding/json"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// sandboxFeatures are the bridge features for sandboxes (isolation spec 10.5).
var sandboxFeatures = []string{"sandbox", "arm"}

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
	case "arm":
		var a struct {
			ID      string                `json:"id"`
			Secrets map[string]api.Secret `json:"secrets"`
		}
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the arm request is not valid JSON", "Update the client."))
			return true
		}
		defer clear(a.Secrets)
		if !validID(a.ID) {
			b.reply(req.ID, nil, UserError("not_found", "no session "+a.ID, "List sessions to find its ID."))
			return true
		}
		if _, err := checkSecrets(a.Secrets); err != nil {
			b.reply(req.ID, nil, err)
			return true
		}
		res, err := HolderRequest(a.ID, "arm", secretsMsg{Env: a.Secrets}, 5*time.Second)
		b.reply(req.ID, res, err)
	default:
		return false
	}
	return true
}
