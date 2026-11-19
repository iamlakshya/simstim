package host

import (
	"context"
	"runtime"

	"github.com/iamlakshya/simstim/internal/api"
)

// CapabilityProbe reports current host prerequisites and registered drivers.
// It retains the caller's cancellation and the collector's bounded deadline.
func CapabilityProbe(ctx context.Context, releaseURL string) (api.CapProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, capabilityTimeout)
	defer cancel()
	probe, err := capabilityProbe(ctx, releaseURL, liveCapSource())
	if err != nil {
		return probe, err
	}
	if runtime.GOOS != "linux" {
		return probe, nil
	}
	if err := writeWallCacheContext(ctx, probe.Walls); err != nil {
		return probe, err
	}
	return probe, nil
}
