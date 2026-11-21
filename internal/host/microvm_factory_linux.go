//go:build linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/wall"
)

// MicroVMResourcesWithPublisherV1 selects one implemented VM backend. The
// caller derives driver from its original protected Need. This selection is
// not a grant and cannot replace its owned operation or native boot receipt.
func MicroVMResourcesWithPublisherV1(driver string, publish func(context.Context, wall.Spec) error) (wall.ResourceFactoryV1, error) {
	switch driver {
	case "chv":
		return CloudHypervisorResourcesWithPublisherV1(publish)
	case "firecracker":
		return FirecrackerResourcesWithPublisherV1(publish)
	case "nitro":
		return NitroResourcesWithPublisherV1(publish)
	default:
		return nil, UserError("wall_unavailable", "the selected VM backend is not implemented by this helper", "Update the host helper or choose a wall reported by host doctor.")
	}
}
