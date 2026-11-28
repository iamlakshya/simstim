//go:build !unix

package chv

import (
	"fmt"
	"os"
)

// SessionDiskAllocatedBytes returns the size of path on non-Unix platforms.
func SessionDiskAllocatedBytes(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat session disk %s: %w", path, err)
	}
	return fi.Size(), nil
}
