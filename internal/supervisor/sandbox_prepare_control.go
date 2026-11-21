package supervisor

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A failed pre-start boot consumes only its already selected control line.
// Earlier requests receive failure before the final cancel ACK; no failed
// start envelope can create a runtime, commit policy or publish readiness.
func (s *Supervisor) readFailedOriginalControlV1(start *originalStartup) {
	defer close(start.readDone)
	for {
		frame, err := start.line.ReadFrame()
		if err != nil {
			return // Only the captured original pidfd can resolve EOF.
		}
		var request proto.Request
		if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil {
			return
		}
		if request.ID == 4 && request.Op == "cancel_start" && len(request.Args) == 0 {
			if err := s.bootOwner.CheckSandboxPrepareCancelV1(start.line); err != nil {
				return
			}
			start.mu.Lock()
			start.cancelRequest = request.ID
			start.mu.Unlock()
			s.cancelOriginalStartup(errStopped)
			return
		}
		if err := s.bootOwner.CheckStartupFrame(start.line); err != nil {
			return
		}
		if request.ID != 2 || request.Op != "secrets" {
			if len(request.Args) != 0 || !(request.ID == 3 && request.Op == host.StartupReadyOperationV1 || request.ID == 5 && request.Op == host.PolicyNextCommitOperationV1) {
				return
			}
		}
		clear(frame.Payload)
		clear(request.Args)
		failure := execRefusal("the original fixed test failed before its selected start completed")
		if err := start.line.WriteFrame(proto.TResponse, proto.Response{ID: request.ID, Error: toProtoErr(failure)}); err != nil {
			return
		}
	}
}
