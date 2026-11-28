package host

import (
	"encoding/json"
	"errors"

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
	"delete": true, "resume": true, "restore": true, "stop": true, "send": true, "peek": true, "rename": true, "kill": true,
	"port.grant": true, "port.revoke": true, "sftp": true, "ssh.exec": true,
}

// refuseInside answers a frame that a bridge inside a session (or another
// cgroup namespace) must refuse: an attach, or a request that acts on a
// session. It reports whether it did.
func (b *bridge) refuseInside(f proto.Frame, callerErr error) bool {
	pe := errCallerInside
	var p *proto.Error
	if errors.As(callerErr, &p) && p != nil {
		pe = p
	}
	switch f.Type {
	case proto.TAttach:
		_ = b.w.WriteJSON(proto.TError, pe)
		return true
	case proto.TRequest:
		var req proto.Request
		if json.Unmarshal(f.Payload, &req) != nil || !sessionOps[req.Op] {
			return false
		}
		b.reply(req.ID, nil, pe)
		return true
	default:
		return false
	}
}
