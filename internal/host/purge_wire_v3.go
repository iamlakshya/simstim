//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const purgeBeginV3 = "purge.begin.v3"
const purgeContinueV3 = "purge.continue.v3"
const purgeAbortV3 = "purge.abort.v3"
const purgeCloseV3 = "purge.close.v3"

type purgeRequestDataV3 struct {
	Version int           `json:"v"`
	Args    api.PurgeArgs `json:"args"`
}

func readPurgeRequestV3(ctx context.Context, line *StartupLineV1, peer *purgeRequestPeerV3) (proto.Request, purgeRequestDataV3, error) {
	var request proto.Request
	var data purgeRequestDataV3
	frame, err := line.ReadFrame()
	if err != nil {
		return request, data, err
	}
	sender, err := line.currentSender()
	if err != nil {
		return request, data, err
	}
	if err := peer.CheckSender(ctx, sender); err != nil {
		return request, data, err
	}
	return decodePurgeRequestDataV3(frame)
}

func decodePurgeRequestDataV3(frame proto.Frame) (request proto.Request, data purgeRequestDataV3, returned error) {
	if frame.Type != proto.TRequest || !api.ExactJSONFields(frame.Payload, request) || json.Unmarshal(frame.Payload, &request) != nil || request.ID == 0 || request.NewAttempt != "" || !api.ExactJSONFields(request.Args, data) || json.Unmarshal(request.Args, &data) != nil || data.Version != 3 || !ValidHostID(data.Args.HostID) || !ValidHostID(data.Args.Nonce) {
		return request, data, placementError("the actual purge request has unknown fixed data")
	}
	if request.Op != purgeBeginV3 && request.Op != purgeContinueV3 && request.Op != purgeAbortV3 && request.Op != purgeCloseV3 {
		return request, data, placementError("the actual purge endpoint does not accept that closed operation")
	}
	return request, data, nil
}

func writePurgeResponseV3(line *StartupLineV1, id uint64, result api.PurgeResult, failure error) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	response := proto.Response{ID: id, OK: failure == nil, Result: raw}
	if failure != nil {
		var wire *proto.Error
		if errors.As(failure, &wire) {
			response.Error = wire
		} else {
			response.Error = UserError("purge", "the original removal still owns unfinished native cleanup: "+failure.Error(), "Keep the host saved and continue the same original removal.")
		}
	}
	return line.WriteFrame(proto.TResponse, response)
}
