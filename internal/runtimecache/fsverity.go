package runtimecache

import (
	"context"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/supply"
)

// installVerityLocked consumes the store's actual no-follow descriptor under
// its existing publication transaction. It never receives a caller path or
// chmods a foreign inode. The transfer's writable descriptor is already closed.
func (s *Store) installVerityLocked(ctx context.Context, item supply.Item) error {
	if item.FSVerity == "" || (item.Kind != supply.KindVMM && item.Kind != supply.KindKernel) {
		return nil
	}
	file, err := s.openItemContext(ctx, item)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return err
	}
	enabled, err := supply.InstallRuntimeVerity(ctx, item, file)
	if err != nil {
		return err
	}
	current, err := s.root.Lstat("sha256/" + item.SHA256)
	if err != nil || !os.SameFile(before, current) || !descriptorSafe(file, current) || !modeMatches(current, Mode(item)) {
		return fmt.Errorf("runtime verity installation changed its owned store identity")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync installed runtime verity: %w", err)
	}
	if enabled && item.Kind == supply.KindVMM && !s.readOnly {
		return s.provisionInstalledVMMV1(ctx, item)
	}
	return nil
}
