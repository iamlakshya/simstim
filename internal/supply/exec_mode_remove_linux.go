//go:build linux

package supply

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// TryRuntimeModeRemovalV1 is cleanup-only. Fresh native exclusivity plus the
// exact self/root/pins intent is required even if an interrupted journal has
// already removed the data inode. It cannot prepare, repair or grant exec.
func TryRuntimeModeRemovalV1(ctx context.Context, artifacts, modes *os.Root, item Item) (*RuntimeModeGuard, error) {
	if modes == nil {
		return nil, nil
	}
	lock, err := cachefs.OpenFile(modes, modeLeaseName(item), os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	held, err := cachefs.TryLock(lock, cachefs.Exclusive)
	if err != nil || !held {
		lock.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrRuntimeModeBusy
	}
	guard := &RuntimeModeGuard{artifacts: artifacts, modes: modes, item: item, lock: lock}
	value, err := readExecModeIntent(lock)
	if err != nil {
		guard.Close()
		return nil, err
	}
	if err := value.checkRecord(item, artifacts, modes, lock); err != nil {
		guard.Close()
		return nil, err
	}
	guard.intent = value
	metadata, err := openModeArtifact(artifacts, item)
	if errors.Is(err, os.ErrNotExist) {
		return guard, ctx.Err()
	}
	if err != nil {
		guard.Close()
		return nil, err
	}
	guard.metadata = metadata
	if err := guard.Recover(ctx); err != nil {
		guard.Close()
		return nil, err
	}
	return guard, nil
}

// RemovalFileV1 lends only the same owned sidecar AFTER the canonical data
// name is absent. The caller holds its existing store transaction and native
// terminal journal admission; no new execution or raw-file permission arises.
func (guard *RuntimeModeGuard) RemovalFileV1(ctx context.Context) (*os.File, error) {
	if guard == nil || guard.closed || guard.lock == nil {
		return nil, fmt.Errorf("runtime mode cleanup guard is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := guard.intent.checkRecord(guard.item, guard.artifacts, guard.modes, guard.lock); err != nil {
		return nil, err
	}
	if _, err := guard.artifacts.Lstat(guard.item.SHA256); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("runtime data still exists or has another cleanup identity")
	}
	if guard.metadata != nil {
		info, err := guard.metadata.Stat()
		if err != nil {
			return nil, err
		}
		if err := checkHeldModeArtifact(guard.intent.Artifact, guard.item, guard.metadata, info.Mode()); err != nil {
			return nil, err
		}
	}
	return guard.lock, nil
}

func (guard *RuntimeModeGuard) RemoveAfterArtifactV1(ctx context.Context) error {
	file, err := guard.RemovalFileV1(ctx)
	if err != nil {
		return err
	}
	if err := guard.intent.Self.CheckCreatedFile(guard.modes, modeLeaseName(guard.item), file, 0o600); err != nil {
		return err
	}
	if err := guard.modes.Remove(modeLeaseName(guard.item)); err != nil {
		return err
	}
	dir, err := guard.modes.Open(".")
	if err != nil {
		return err
	}
	err = dir.Sync()
	return errors.Join(err, dir.Close())
}
