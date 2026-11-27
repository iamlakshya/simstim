//go:build !linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"github.com/iamlakshya/simstim/internal/wall"
)

func (*bridge) gvisorProbeContextV1(proto.Request, supply.Manifest) (context.Context, func() error, error) {
	return nil, nil, UserError("wall_unavailable", "the physical gvisor probe requires a Linux host", "Use a Linux host for this sandbox.")
}

func probeReservedGvisorV1(context.Context, *SessionOperation) (wall.Probe, error) {
	return wall.Probe{}, UserError("wall_unavailable", "the native gvisor preflight requires a Linux host", "Use a Linux host for this sandbox.")
}
