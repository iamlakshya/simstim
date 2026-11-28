//go:build linux

package host

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitBeforeReap blocks until child pid has exited without reaping it, keeping
// pid and its process group reserved until cmd.Wait reaps the zombie.
func waitBeforeReap(pid int) {
	if pid <= 0 {
		return
	}
	var siginfo unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &siginfo, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return
		}
	}
}
