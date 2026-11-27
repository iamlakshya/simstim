package supervisor

import (
	"encoding/json"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// request answers a request, or passes it to the holder (spec 10.4).
func (s *Supervisor) request(c *client, f proto.Frame) bool {
	var req proto.Request
	if json.Unmarshal(f.Payload, &req) != nil {
		return false
	}
	if handled, alive := s.debugTraceRequestV1(c, req, f); handled {
		return alive
	}
	if s.traceUpperRequestV1(c, req) {
		return true
	}
	if handled, keep := s.noneDebugRequestV1(c, req, f); handled {
		return keep
	}
	if s.nativeStopRequestV1(c, req) {
		return true
	}
	if s.nativeWarmResumeRequestV1(c, req) {
		return true
	}
	if s.editorSSHViewRequestV1(c, req) {
		return true
	}
	if s.portRequest(c, req) {
		return true
	}
	if s.modelUsageRequest(c, req) {
		return true
	}
	if s.packageSettingsRequest(c, req) {
		return true
	}
	if s.namedSSHKeyRequest(c, req) {
		return true
	}
	if s.armRequest(c, req) {
		return true
	}
	if s.copyRequest(c, req) {
		return true
	}
	if s.imageSaveRequestV1(c, req) {
		return true
	}
	if s.checkpointRequestV1(c, req) {
		return true
	}
	if s.pullRequest(c, req) {
		return true
	}
	if s.sharedPolicyRequest(c, req) {
		return true
	}
	if s.memberRequest(c, req) {
		return true
	}
	if s.protectedCohortReadinessRequestV1(c, req) {
		return true
	}
	switch req.Op {
	case "info":
		var infoArgs api.SandboxCohortInfoArgsV1
		if len(req.Args) > 0 {
			if !api.ExactJSONFields(req.Args, infoArgs) || json.Unmarshal(req.Args, &infoArgs) != nil {
				c.reply(req.ID, nil, toProtoErr(secretIntentFailure()))
				return true
			}
		}
		m, err := s.snapshotForInfo()
		if !infoArgs.Cohort {
			m.SecretState = nil
		}
		if err != nil {
			c.reply(req.ID, nil, toProtoErr(err))
		} else {
			c.reply(req.ID, m, nil)
		}
	case "rename":
		var a struct {
			Name string `json:"name"`
		}
		err := json.Unmarshal(req.Args, &a)
		name := strings.TrimSpace(a.Name)
		if err != nil || name == "" {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "a name is needed", Hint: "Give the session a name."})
			return true
		}

		if err := s.rename(name); err != nil {
			c.reply(req.ID, nil, renameFailure(err))
			return true
		}
		s.audit.write("rename", nil)
		c.reply(req.ID, map[string]string{"name": name}, nil)
	case "stop":
		s.requestTrustedStopV1()
		c.reply(req.ID, map[string]bool{"stopping": true}, nil)
		s.signalTrustedStopV1()
	case "kill":
		c.reply(req.ID, map[string]bool{"killed": true}, nil)
		select {
		case s.quit <- "killed by request":
		default:
		}
	case "sockets":
		c.reply(req.ID, host.SocketsState(s.peerCheck), nil)
	case "hook":
		c.reply(req.ID, nil, host.ErrNoHooksHere)
	case "secrets":
		c.reply(req.ID, nil, &proto.Error{Code: "started", Message: "the session has started, so it takes no secrets now",
			Hint: "Resume the session to pass its secrets again."})
	case "peek":
		return s.pass(c, f)
	case "send":
		var a struct {
			Text  string `json:"text"`
			Enter bool   `json:"enter"`
		}
		if json.Unmarshal(req.Args, &a) != nil {
			c.reply(req.ID, nil, &proto.Error{Code: "bad_request", Message: "text is needed", Hint: "Pass the text to send."})
			return true
		}
		data := []byte(a.Text)
		if a.Enter {
			// The holder types the Enter too, and counts it.
			data = append(data, '\r')
		}
		s.typed(data)
		return s.pass(c, f)
	default:
		if strings.HasPrefix(req.Op, "debug.") {
			c.reply(req.ID, nil, &proto.Error{Code: "unknown_op", Message: "a session in a sandbox has no debug yet",
				Hint: "Read the session's supervisor.log in its folder on the host."})
			return true
		}
		c.reply(req.ID, nil, &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op, Hint: "Update the client."})
	}
	return true
}
