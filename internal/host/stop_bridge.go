package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, "session.stop") }

func (b *bridge) handleStopV1(request proto.Request) bool {
	if request.Op != "stop" {
		return false
	}
	var args struct {
		ID string `json:"id"`
	}
	fields, err := api.JSONObjectFields(request.Args, "id")
	if err != nil || len(fields) != 1 || !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || !validID(args.ID) {
		b.reply(request.ID, nil, UserError("bad_request", "stop needs one exact session ID", "List sessions and retry the selected sandbox with the current client."))
		return true
	}
	row, err := StopContext(b.ctx, args.ID)
	b.reply(request.ID, row, stopRequestFailureV1(err))
	return true
}

func stopRequestFailureV1(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var failure *proto.Error
	if errors.As(err, &failure) {
		return err
	}
	return fmt.Errorf("%w: %w", UserError("stop_failed", "the sandbox could not complete its stop", "Check the host with host doctor and retry stop through the current client."), err)
}
