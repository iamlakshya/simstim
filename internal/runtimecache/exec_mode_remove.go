package runtimecache

import (
	"context"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
)

// This storage-only interface carries the existing actual native cleanup
// guard. It is not a callback issuer or an execution/slot admission surface.
type modeRemovalV1 interface {
	file(context.Context) (*os.File, error)
	remove(context.Context) error
	Close() error
}

func (s *Store) removeModeAfterPrune(ctx context.Context, item supply.Item) error {
	guard, err := s.prepareModeRemoval(ctx, item)
	if err != nil || guard == nil {
		return err
	}
	defer guard.Close()
	return guard.remove(ctx)
}

func (s *Store) purgeModeAfterData(ctx context.Context, item supply.Item, remove func(PurgeEntry) error) error {
	guard, err := s.prepareModeRemoval(ctx, item)
	if err != nil || guard == nil {
		return err
	}
	defer guard.Close()
	file, err := guard.file(ctx)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return remove(PurgeEntry{Name: "execmode/" + item.SHA256 + ".lease", Info: info})
}
