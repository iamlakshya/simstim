//go:build unix

package chv

import (
	"fmt"
	"os"
	"syscall"
)

// SessionDiskAllocatedBytes returns the real disk space allocated to path on
// the host filesystem (matching `du` via st_blocks * 512), rather than the
// apparent size of a sparse session disk (isolation spec 17.1).
func SessionDiskAllocatedBytes(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat session disk %s: %w", path, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512, nil
	}
	return fi.Size(), nil
}
