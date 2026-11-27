//go:build linux

package jail

import (
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
)

// Serial regression seams retain the exact creation descriptor. Production
// uses the kernel directly; no PID lookup or alternate kill path exists here.
var ownedPoll = unix.Poll
var ownedWaitid = unix.Waitid
var ownedGroupSignal = unix.Kill
var ownedPidfdSignal = unix.PidfdSendSignal

func ownedPIDFDReady(fd, timeout int) (bool, error) {
	for {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := ownedPoll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if n == 0 {
			return false, nil
		}
		if n != 1 || fds[0].Revents & ^int16(unix.POLLIN|unix.POLLHUP) != 0 || fds[0].Revents&unix.POLLIN == 0 {
			return false, fmt.Errorf("owned jail pidfd has no valid exit readiness: %#x", fds[0].Revents)
		}
		return true, nil
	}
}

func waitOwnedPIDFD(fd, timeout int) error {
	for {
		ready, err := ownedPIDFDReady(fd, timeout)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
	}
}
