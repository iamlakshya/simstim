//go:build unix

package diag

import "golang.org/x/sys/unix"

// sysMono reads the shared kernel CLOCK_MONOTONIC at the actual event.
func sysMono() int64 {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return 0
	}
	return ts.Nano()
}
