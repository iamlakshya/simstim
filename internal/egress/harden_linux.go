//go:build linux

package egress

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// hardenProcess clears the Linux dumpable flag and disables core dumps before
// the egress process reads any policy or secret values (spec 13, P1.7).
func hardenProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("egress: set PR_SET_DUMPABLE=0: %w. Try: check kernel or container prctl permissions", err)
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{Cur: 0, Max: 0}); err != nil {
		return fmt.Errorf("egress: set RLIMIT_CORE=0: %w. Try: check process rlimit permissions", err)
	}
	return nil
}

// processDumpable reports the current PR_GET_DUMPABLE value on Linux.
func processDumpable() (int, error) {
	v, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return -1, fmt.Errorf("egress: read PR_GET_DUMPABLE: %w", err)
	}
	return v, nil
}
