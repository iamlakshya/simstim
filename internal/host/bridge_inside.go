package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/proto"
)

// A program in a session can run the helper's bridge itself. The holders
// refuse it at their sockets (holder_peer.go), but the bridge also stops
// and removes sessions through systemd. So a bridge that runs inside a
// session (CheckCaller) refuses every request that acts on a session, and
// every attach (spec 10.4). Delete, Resume and Rename check again, as the
// helper's other commands reach them too.

// sessionOps are the bridge's requests that act on a session.
var sessionOps = map[string]bool{
	"delete": true, "resume": true, "restore": true, "send": true, "peek": true, "rename": true, "kill": true,
}

// refuseInside answers a frame that a bridge inside a session must refuse:
// an attach, or a request that acts on a session. It reports whether it
// did.
func (b *bridge) refuseInside(f proto.Frame) bool {
	switch f.Type {
	case proto.TAttach:
		_ = b.w.WriteJSON(proto.TError, errCallerInside)
		return true
	case proto.TRequest:
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil || !sessionOps[req.Op] {
			return false
		}
		b.reply(req.ID, nil, errCallerInside)
		return true
	default:
		return false
	}
}
