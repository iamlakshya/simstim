//go:build !linux && !darwin

package client

import (
	"context"
	"os/exec"
)

func imageVerifierCommand(context.Context, *imageVerifierStage) (*exec.Cmd, func() error, error) {
	return nil, nil, imageVerifierUnavailable()
}
