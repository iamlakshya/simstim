//go:build unix

package diag

import "golang.org/x/sys/unix"

// sysMono reads CLOCK_MONOTONIC once, at start: Now adds Go's own fast
// monotonic clock to it.
func sysMono() int64 {
	var ts unix.Timespec
	if unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts) != nil {
		return 0
	}
	return ts.Nano()
}
