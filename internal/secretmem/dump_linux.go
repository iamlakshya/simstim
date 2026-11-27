//go:build linux

package secretmem

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func setNonDumpable() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("mark process non-dumpable: %w", err)
	}
	return nil
}

func currentDumpable() (*int, error) {
	value, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("read process dumpability: %w", err)
	}
	return &value, nil
}
