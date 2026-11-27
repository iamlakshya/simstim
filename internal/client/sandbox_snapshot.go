package client

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// SandboxSnapshot reads complete structural membership. H5 separately matches
// known local lineage before names, values or immutable authority are used.
func (c *Conn) SandboxSnapshot(ctx context.Context, parentID string) (*api.SandboxSnapshot, error) {
	if !c.HasFeature(api.FeatureSpawn) || !c.HasFeature(api.FeatureSandboxSnapshot) {
		return nil, &UserError{Msg: "the helper cannot bind complete sandbox membership", Hint: "Upgrade it, then stop and resume the sandbox.", Code: "upgrade_required"}
	}
	var raw json.RawMessage
	if err := c.Call(ctx, "sandbox.snapshot", api.SandboxSnapshotArgs{ID: parentID}, &raw); err != nil {
		return nil, err
	}
	snapshot, err := api.DecodeSandboxSnapshot(raw, parentID, proto.MaxFrame)
	if err != nil {
		return nil, &UserError{Msg: "the host returned an invalid sandbox snapshot", Hint: "Update the helper and list sessions again.", Code: "protocol"}
	}
	return snapshot, nil
}
