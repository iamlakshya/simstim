//go:build linux

package classify

import (
	"errors"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

func commandSupported() bool { return true }

type ownedCommand struct {
	cmd         *exec.Cmd
	fd          int
	mu          sync.Mutex
	groupUnsafe bool
}

func commandGroup(cmd *exec.Cmd) commandProcess {
	owned := &ownedCommand{cmd: cmd, fd: -1}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, PidFD: &owned.fd}
	return owned
}

func (owned *ownedCommand) waitExited() error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PIDFD, owned.fd, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			owned.mu.Lock()
			owned.groupUnsafe = true
			owned.mu.Unlock()
		}
		return err
	}
}

func (owned *ownedCommand) stop() {
	owned.mu.Lock()
	defer owned.mu.Unlock()
	if owned.fd < 0 || owned.cmd.Process == nil {
		return
	}
	if !owned.groupUnsafe {
		var info unix.Siginfo
		var err error
		for {
			err = unix.Waitid(unix.P_PIDFD, owned.fd, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
			if !errors.Is(err, unix.EINTR) {
				break
			}
		}
		if err == nil {
			_ = unix.Kill(-owned.cmd.Process.Pid, unix.SIGKILL)
			_ = unix.PidfdSendSignal(owned.fd, unix.SIGKILL, nil, 0)
			return
		}
		owned.groupUnsafe = true
	}
	_ = unix.PidfdSendSignal(owned.fd, unix.SIGKILL, nil, 0)
}

func (owned *ownedCommand) close() {
	if owned.fd >= 0 {
		_ = unix.Close(owned.fd)
		owned.fd = -1
	}
}
