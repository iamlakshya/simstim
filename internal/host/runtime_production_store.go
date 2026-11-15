package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

// openRuntimeProductionStore uses this process's original admitted host
// placement and keeps its active native reservation through all cache cleanup.
func openRuntimeProductionStore(ctx context.Context) (*runtimePublication, *runtimecache.Store, error) {
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, nil, err
	}
	store, err := runtimecache.OpenHost(publication.ctx, paths.ArtefactsDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		return nil, nil, errors.Join(err, publication.close())
	}
	return publication, store, nil
}

func (part *runtimeOwnedPart) close() error {
	if part == nil {
		return nil
	}
	return errors.Join(part.part.Close(), part.store.Close(), part.publication.close())
}
