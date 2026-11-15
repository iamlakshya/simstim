package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

// CapabilityProbe reports current host prerequisites and registered drivers.
// It retains the caller's cancellation and the collector's bounded deadline.
func CapabilityProbe(ctx context.Context, releaseURL string) (api.CapProbe, error) {
	return capabilityProbe(ctx, releaseURL, liveCapSource())
}
