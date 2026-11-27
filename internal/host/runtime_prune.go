package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

// PruneRuntime applies runtime retention. Its session references are read
// under the store transaction and incoming transfers are never enumerated.
func PruneRuntime(ctx context.Context, dryRun bool) ([]string, error) {
	if !dryRun {
		if err := CheckCaller(); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.ArtefactsDir(), "sha256")); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, runtimeFailure(fmt.Errorf("inspect runtime cache: %w", err))
	}
	var store *runtimecache.Store
	var err error
	if dryRun {
		store, err = runtimecache.OpenReadOnly(paths.ArtefactsDir())
	} else {
		store, err = runtimecache.Open(paths.ArtefactsDir())
	}
	if err != nil {
		return nil, runtimeFailure(err)
	}
	defer store.Close()
	removed, err := store.PruneWithReferences(ctx, runtime.GOARCH, runtimeReferences, time.Now(), dryRun)
	if err != nil {
		return removed, runtimeFailure(err)
	}
	return removed, nil
}

func oldRuntimeFix() []fixItem {
	old, err := PruneRuntime(context.Background(), true)
	if err != nil {
		return []fixItem{{Fix: api.Fix{ID: "old-runtime", Problem: "the runtime cache cannot be checked safely", Action: "check its files and the session manifests before pruning", Manual: true}}}
	}
	if len(old) == 0 {
		return nil
	}
	return []fixItem{{Fix: api.Fix{ID: "old-runtime", Problem: fmt.Sprintf("%d old runtime files are no longer retained", len(old)), Action: "remove them and keep the two newest runtime labels, session references and files from the last hour", Command: brand.HostBinary + " prune"}, run: func() error { _, err := PruneRuntime(context.Background(), false); return err }}}
}
