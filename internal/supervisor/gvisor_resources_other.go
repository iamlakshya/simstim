//go:build !linux

package supervisor

import (
	"context"

	"github.com/iamlakshya/simstim/internal/host"
)

func prepareGVisor(ctx context.Context, m *host.Manifest) (sandbox, error) {
	if _, err := originalGvisorPlan(ctx, m); err != nil {
		return nil, err
	}
	return nil, execRefusal("the gvisor runtime needs the Linux host helper")
}
