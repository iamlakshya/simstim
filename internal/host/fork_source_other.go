//go:build !linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

func forkContextV1(context.Context, api.ForkArgs) (*api.ForkResult, error) {
	return nil, forkFailureV1("fork_off", "fork needs a supported Linux host and its original native sandbox controller")
}
