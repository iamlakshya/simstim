//go:build linux

package runtimecache

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

type linuxModeRemovalV1 struct {
	guard            *supply.RuntimeModeGuard
	artifacts, modes *os.Root
}

func (guard *linuxModeRemovalV1) file(ctx context.Context) (*os.File, error) {
	return guard.guard.RemovalFileV1(ctx)
}
func (guard *linuxModeRemovalV1) remove(ctx context.Context) error {
	return guard.guard.RemoveAfterArtifactV1(ctx)
}
func (guard *linuxModeRemovalV1) Close() error {
	return errors.Join(guard.guard.Close(), guard.artifacts.Close(), guard.modes.Close())
}

func (s *Store) prepareModeRemoval(ctx context.Context, item supply.Item) (modeRemovalV1, error) {
	modes, err := cachefs.OpenPrivateChild(s.root, "execmode", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	artifacts, err := cachefs.OpenPrivateChild(s.root, "sha256", false)
	if err != nil {
		modes.Close()
		return nil, err
	}
	guard, err := supply.TryRuntimeModeRemovalV1(ctx, artifacts, modes, item)
	if err != nil || guard == nil {
		if guard != nil {
			guard.Close()
		}
		artifacts.Close()
		modes.Close()
		return nil, err
	}
	return &linuxModeRemovalV1{guard: guard, artifacts: artifacts, modes: modes}, nil
}
