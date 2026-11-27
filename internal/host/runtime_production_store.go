package host

import (
	"context"
	"os"

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
	var openControl *placementControl
	var openGuard *os.Root
	openPhase := true
	gate := func(gateCtx context.Context, change func() error) error {
		return publication.gateControlRoot(gateCtx, func(control *placementControl, guard *os.Root) error {
			if !openPhase {
				return change()
			}
			openControl, openGuard = control, guard
			defer func() {
				openControl, openGuard = nil, nil
				openPhase = false
			}()
			return change()
		})
	}
	//nolint:contextcheck // publication.ctx derives from ctx and cancels on drain
	store, err := runtimecache.OpenHostCreated(publication.ctx, paths.ArtefactsDir(), publication.owner.domain(), gate, func(path string, info os.FileInfo) error {
		if openControl == nil || openGuard == nil {
			return placementError("storage directory creation occurred outside its active placement lock")
		}
		return recordStorageDirV1(openControl, openGuard, path, info)
	})
	// The caller adopted an owner before entering this factory. Return every
	// actual partial object; only that owner can close store before publication.
	if err != nil {
		return publication, store, err
	}
	return publication, store, nil
}

func (part *runtimeOwnedPart) close() error {
	if part == nil {
		return nil
	}
	part.mu.Lock()
	defer part.mu.Unlock()
	part.closing = true
	if part.part != nil {
		if err := part.part.Close(); err != nil {
			return err
		}
		part.part = nil
	}
	if part.store != nil {
		if err := part.store.Close(); err != nil {
			return err
		}
		part.store = nil
	}
	if part.publication != nil {
		if err := part.publication.close(); err != nil {
			return err
		}
		part.publication = nil
	}
	return nil
}
