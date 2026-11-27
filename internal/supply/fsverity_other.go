//go:build !linux

package supply

import (
	"context"
	"os"
)

// InstallRuntimeVerity leaves filesystems outside Linux unchanged. Runtime
// execution and its sealed-copy fallback are Linux-only consumer operations.
func InstallRuntimeVerity(ctx context.Context, _ Item, _ *os.File) (bool, error) {
	return false, ctx.Err()
}
