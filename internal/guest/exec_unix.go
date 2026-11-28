//go:build unix

package guest

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func setExecSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killExecGroup(pid int, reaped <-chan struct{}) {
	if pid <= 0 {
		return
	}
	select {
	case <-reaped:
		return
	default:
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case <-reaped:
	case <-t.C:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

func classifyExecStartErr(err error) int {
	if errors.Is(err, syscall.ENOENT) {
		return 127
	}
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOEXEC) || errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.ENOTDIR) {
		return 126
	}
	return 125
}

func exitCodeFromWaitErr(err error) int {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 125
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	return 125
}
