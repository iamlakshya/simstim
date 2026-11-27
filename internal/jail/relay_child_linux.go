package jail

import (
	"errors"

	"golang.org/x/sys/unix"
)

// waitChildWithRelay keeps the owned child unreaped until forwarding has
// been disarmed. The callbacks keep the reaping boundary explicit.
func waitChildWithRelay(pid int, relay *sigRelay, reap, kill func() error) error {
	err := waitExited(unix.P_PID, pid, 0)
	relay.stop()
	if err != nil && !errors.Is(err, unix.ECHILD) {
		_ = kill()
	}
	_ = reap()
	return err
}
