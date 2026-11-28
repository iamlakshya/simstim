//go:build linux

package guest

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reexecAfterLandlock replaces the current process image in place with the
// same executable and arguments, using cleanEnv (without the Landlock env var).
//
// On kernels without LANDLOCK_RESTRICT_SELF_TSYNC (Linux < 6.19, including ABI
// 6), go-landlock applies landlock_restrict_self to each Go OS thread via psx,
// which creates distinct sibling Landlock domains per thread. Under
// LANDLOCK_SCOPE_SIGNAL, sibling domains cannot signal children spawned by
// another thread (returning EPERM on kill_session, stopHolders and exec
// timeouts). Re-execing once collapses the process back to one thread inside
// a single Landlock domain while preserving PID 1, fd 3, seccomp and Landlock
// confinement across all future threads and child processes.
func reexecAfterLandlock(cleanEnv []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("guest: locate executable for landlock re-exec: %w", err)
	}
	if err := unix.Exec(exe, os.Args, cleanEnv); err != nil {
		return fmt.Errorf("guest: re-exec after landlock: %w", err)
	}
	return nil
}
