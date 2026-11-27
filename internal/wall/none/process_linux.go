//go:build linux

package none

import (
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/proc"
	"golang.org/x/sys/unix"
	"syscall"
)

func unwalledSocketPairV1() ([2]int, error) {
	return unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
}

func unwalledBirthV1(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	return proc.LinuxBirthTicksV1(raw)
}

func unwalledAttributes(pidfd *int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGTERM, PidFD: pidfd}
}
func closeUnwalledHandle(fd int) error {
	if fd < 0 {
		return nil
	}
	return unix.Close(fd)
}
func checkUnwalledHandle(fd int) error {
	if fd < 0 {
		return failure("the original unwalled init has no native creation handle", nil)
	}
	count, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 0)
	if err != nil || count != 0 {
		return failure("the original unwalled init has ended", err)
	}
	return nil
}
