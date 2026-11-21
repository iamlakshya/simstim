//go:build darwin

package client

import (
	"context"
	"os/exec"
	"path/filepath"
)

func imageVerifierCommand(ctx context.Context, stage *imageVerifierStage) (*exec.Cmd, func() error, error) {
	if err := stage.check(ctx); err != nil {
		return nil, nil, err
	}
	// Darwin executes the private creator-owned stage by name. The original
	// directory and descriptor remain held and checked until this child joins.
	// This does not defend another process with the laptop user's own identity.
	return exec.CommandContext(ctx, filepath.Join(stage.root.Name(), "verifier"), "--image-signature-v1"), func() error { return nil }, nil
}
