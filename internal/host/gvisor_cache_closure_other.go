//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
)

func validateGvisorCachePublicationV1(ctx context.Context, probes []api.WallProbe) error {
	for _, probe := range probes {
		if probe.Driver == "gvisor" && probe.OK {
			return newRuntimeError("physical gvisor facts require their original Linux native producer")
		}
	}
	return ctx.Err()
}
