package supervisor

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/phaseclock"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (s *Supervisor) abortPreparedMember(args proto.CtlStartArgs, target string) error {
	ctx, cancel := phaseclock.WithDeadline(s.lifetimeContext(), now().Add(clientWriteWait))
	defer cancel()
	return s.abortPreparedMemberContext(ctx, args, target)
}
func (s *Supervisor) abortPreparedMemberContext(ctx context.Context, args proto.CtlStartArgs, target string) error {
	if s.ctl == nil {
		return execRefusal("prepared member cleanup has no owned control stream")
	}
	var raw json.RawMessage
	if err := s.ctl.requestDetached(ctx, proto.CtlMemberAbort, proto.CtlMemberPrepareArgs{Version: 1, Target: target, Start: args}, &raw); err != nil {
		s.failMember()
		return err
	}
	var result proto.CtlMemberAbortResult
	if !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 {
		s.failMember()
		return execRefusal("prepared member cleanup returned an invalid acknowledgement")
	}
	switch result.State {
	case "absent", "aborted", "failed":
		return nil
	case "consumed", "busy":
		s.failMember()
		return &proto.Error{Code: "member_uncertain", Message: "the prepared member cleanup could not confirm an idle released reservation", Hint: "Inspect this exact member before retrying. Its possibly started process and other live members have not been stopped."}
	default:
		s.failMember()
		return execRefusal("prepared member cleanup returned an unsupported state")
	}
}
