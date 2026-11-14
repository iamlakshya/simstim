package client

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
)

// EnsureAgentsDiskV1 obtains the actual canonical producer's exact disk on
// this authenticated connection. It supplies no source path or program choice.
// Host-installed code stays outside G11; the client selects these precise
// bytes for the Need bound by H5, rather than trusting generic inventory pins.
func (c *Conn) EnsureAgentsDiskV1(ctx context.Context, arch string) (image.AgentDiskV1, error) {
	var disk image.AgentDiskV1
	if !c.HasFeature(api.FeatureAgentsDiskV1) {
		return disk, c.grantHelperOld("build the installed agents disk")
	}
	if arch != "amd64" && arch != "arm64" {
		return disk, &UserError{Msg: "the agents disk architecture is unsupported", Hint: "Use a Linux host on amd64 or arm64.", Code: "agents_disk", Kind: KindFailed}
	}
	if err := c.Call(ctx, "agents.disk.ensure", api.AgentsDiskRequestV1{Version: 1, Arch: arch}, &disk); err != nil {
		return disk, err
	}
	if err := disk.Validate(arch); err != nil {
		return disk, imageClientError(err)
	}
	return disk, nil
}

// RefreshInstalledAgentsV1 is the ordinary observed-installer-completion hook.
// Older helpers keep their existing installation behaviour. The conversion
// uses the caller's real lifetime, independent of a shorter dial/check timeout.
func (c *Conn) RefreshInstalledAgentsV1(ctx context.Context) error {
	if !c.HasFeature(api.FeatureAgentsDiskV1) {
		return nil
	}
	probe, err := c.CapProbe(ctx, "")
	if err != nil {
		return err
	}
	_, err = c.EnsureAgentsDiskV1(ctx, probe.Arch)
	return err
}
