//go:build linux

package runtimecache

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// One pending actual metadata constructor belongs to this existing Store.
// It is the same execmode registry, not another durable execution authority.
type runtimeModeProvisionV1 struct {
	guard            *supply.RuntimeModeGuard
	artifacts, modes *os.Root
}

func (owner *runtimeModeProvisionV1) Close() error {
	if owner == nil {
		return nil
	}
	if owner.guard != nil {
		if err := owner.guard.Close(); err != nil {
			return err
		}
		owner.guard = nil
	}
	for _, root := range []**os.Root{&owner.artifacts, &owner.modes} {
		if *root != nil {
			if err := (*root).Close(); err != nil {
				return err
			}
			*root = nil
		}
	}
	return nil
}

func (s *Store) provisionInstalledVMMV1(ctx context.Context, item supply.Item) error {
	if s.pendingMode != nil {
		if err := s.pendingMode.Close(); err != nil {
			return err
		}
		s.pendingMode = nil
	}
	owner := &runtimeModeProvisionV1{}
	s.pendingMode = owner // Adopt before every later namespace/metadata error.
	var err error
	owner.artifacts, err = cachefs.OpenPrivateChild(s.root, "sha256", false)
	if err != nil {
		return err
	}
	owner.modes, err = cachefs.OpenPrivateChild(s.root, "execmode", true)
	if err != nil {
		return err
	}
	owner.guard, err = supply.EnsureRuntimeModeIntentV1(ctx, owner.artifacts, owner.modes, item)
	if err != nil {
		return err
	}
	if err := owner.Close(); err != nil {
		return err
	}
	s.pendingMode = nil
	return nil
}
