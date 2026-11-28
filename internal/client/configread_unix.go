//go:build unix

package client

import (
	"fmt"
	"os"
	"syscall"
)

// openConfigNoBlock opens path with O_NONBLOCK so a FIFO never hangs a config
// read before Stat can reject it (docs/contributing/cli-spec.md 13.1).
func openConfigNoBlock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return f, nil
}
