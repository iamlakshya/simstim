//go:build !linux

package runtimecache

import (
	"context"
	"github.com/iamlakshya/simstim/internal/supply"
)

func (s *Store) artifactModeGuard(context.Context, supply.Item) (any, func(), error) {
	return nil, func() {}, nil
}
func retryExecMode(ctx context.Context, run func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return run()
}
func (s *Store) checkPruneModeIntent(string, bool) error { return nil }
func (s *Store) prepareModeRemoval(context.Context, supply.Item) (modeRemovalV1, error) {
	return nil, nil
}
