package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/api"
)

// Checkpoints returns immutable checkpoint names, newest first. A later
// rewind sends the selected name, so a new turn cannot shift its index.
func (c *Conn) Checkpoints(ctx context.Context, id string) (api.CheckpointsResult, error) {
	if !api.ValidSessionID(id) {
		return api.CheckpointsResult{}, forkCheckpointProtocol()
	}
	if c == nil || !c.HasFeature(api.FeatureRewind) {
		return api.CheckpointsResult{}, forkCheckpointUpgrade("rewind sandboxes")
	}
	var result api.CheckpointsResult
	if err := c.Call(ctx, api.OpCheckpoints, api.CheckpointsArgs{ID: id}, &result); err != nil {
		return result, err
	}
	if err := result.Validate(id); err != nil {
		return api.CheckpointsResult{}, forkCheckpointProtocol()
	}
	return result.Clone(), nil
}

// Rewind restores one previously selected immutable checkpoint. The host
// makes an undo checkpoint before replacing the disk and resuming cold.
func (c *Conn) Rewind(ctx context.Context, args api.RewindArgs) (_ api.RewindResult, returned error) {
	if err := args.Validate(); err != nil {
		return api.RewindResult{}, &UserError{Code: "rewind_unknown", Msg: "invalid checkpoint selection", Hint: "List checkpoints and select one of their numbers.", Kind: KindNotFound}
	}
	if c == nil || !c.HasFeature(api.FeatureRewind) {
		return api.RewindResult{}, forkCheckpointUpgrade("rewind sandboxes")
	}
	standalone := grantRequestOf(ctx) == nil
	ctx, request := ensureGrantRequest(ctx)
	if standalone {
		defer func() { returned = errors.Join(returned, request.Close()) }()
	}
	prepared, err := c.PrepareRewind(ctx, args, RewindOptions{Terminal: InputAllowed(ctx)})
	if err != nil {
		return api.RewindResult{}, err
	}
	return prepared.Commit(ctx)
}
