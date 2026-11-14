//go:build linux

package process

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// pollInit is replaced only by serial ownership regression tests. Its caller
// retains the exact owned pidfd; no numeric PID recovery is permitted.
var pollInit = unix.Poll
var signalInit = unix.PidfdSendSignal

func pidfdExitReady(n int, event int16) (bool, error) {
	if n == 0 {
		return false, nil
	}
	if n != 1 || event & ^int16(unix.POLLIN|unix.POLLHUP) != 0 {
		return false, fmt.Errorf("owned namespace-init pidfd has invalid readiness %#x", event)
	}
	if event&unix.POLLIN == 0 {
		return false, fmt.Errorf("owned namespace-init pidfd has no exit readiness: %#x", event)
	}
	return true, nil
}

// waitInitExit accepts only POLLIN from the retained owned pidfd. Errors and
// unrelated readiness retain uncertainty; EINTR merely retries the same wait.
func waitInitExit(fd int) error {
	for {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := pollInit(poll, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("wait for owned namespace-init exit: %w", err)
		}
		ready, err := pidfdExitReady(n, poll[0].Revents)
		if err != nil || ready {
			return err
		}
	}
}

func pollInitExit(fd int) (bool, error) {
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	n, err := pollInit(poll, 0)
	if err != nil {
		return false, fmt.Errorf("inspect owned namespace-init exit: %w", err)
	}
	ready, checkErr := pidfdExitReady(n, poll[0].Revents)
	return ready, checkErr
}

func initExited(fd int) bool { ready, err := pollInitExit(fd); return err == nil && ready }
