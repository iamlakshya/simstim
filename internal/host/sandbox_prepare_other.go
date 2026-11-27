//go:build !linux

package host

import (
	"context"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func (b *bridge) runSandboxPreparePlatformV1(_ context.Context, _ time.Time, _ *sandboxPrepareWitnessV1) (api.SandboxPrepareResultV1, error) {
	return api.SandboxPrepareResultV1{}, UserError("wall_unavailable", "sandbox preparation requires a Linux host", "Connect to a Linux amd64 or arm64 host before running "+brand.Name+" host prepare.")
}
