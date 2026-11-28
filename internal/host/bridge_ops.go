package host

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// moreFeatures are the features that set hosts and sessions up: the requests
// in this file, a new session's secrets (secrets.go), its missing folder
// (folders.go), trust in a repository's setup script (setup.go), the resume
// of a session whose agent ended (resume.go), stopping a session while
// keeping its state (stop.go), SFTP/SSH-exec/bundle-pull/seed file transfers,
// and port grants for walled sessions (bridge_ports.go).
var moreFeatures = []string{"agents.check", "doctor.fix", "secrets", "mkdir", "setup.trust", "resume", "stop", api.FeatureSFTP, api.FeatureSSHExec, api.FeaturePullBundle, api.FeatureSeed, api.FeaturePulled, api.FeatureEvents, "port.grant"}

func init() { Features = append(Features, moreFeatures...) }

// handleMore serves the requests that set a host up, and the resume of a
// session. It reports whether it knew the request.
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
	case "resume":
		var a api.ResumeArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the resume request is not valid JSON", "Update the client."))
			return true
		}
		v, err := Resume(a)
		b.reply(req.ID, v, err)
	case "stop":
		var a api.StopArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the stop request is not valid JSON", "Update the client."))
			return true
		}
		v, err := Stop(a)
		b.reply(req.ID, v, err)
	case "port.grant", "port.revoke":
		b.handlePortGrant(req)
	case "session.start":
		var a api.SessionStartArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the session.start request is not valid JSON", "Update the client."))
			return true
		}
		v, err := StartDeferred(a)
		b.reply(req.ID, v, err)
	case "copy.apply":
		var a api.CopyApplyArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the copy.apply request is not valid JSON", "Update the client."))
			return true
		}
		err := ApplyCopyIn(a)
		b.reply(req.ID, map[string]bool{"ok": true}, err)
	case "prune":
		var a api.PruneArgs
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &a); err != nil {
				b.reply(req.ID, nil, UserError("bad_request", "the prune request is not valid JSON", "Update the client."))
				return true
			}
		}
		v, err := PruneSessions(a)
		b.reply(req.ID, v, err)
	case "session.finish":
		var a api.SessionFinishArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the session.finish request is not valid JSON", "Update the client."))
			return true
		}
		v, err := FinishSession(a.ID)
		b.reply(req.ID, v, err)
	case api.FeaturePulled:
		var a api.PulledArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			b.reply(req.ID, nil, UserError("bad_request", "the pulled request is not valid JSON", "Update the client."))
			return true
		}
		err := RecordPulled(a)
		if err != nil {
			b.reply(req.ID, nil, err)
			return true
		}
		b.reply(req.ID, map[string]bool{"ok": true}, nil)
	default:
		return false
	}
	return true
}
