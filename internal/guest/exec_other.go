//go:build !unix

package guest

import (
	"errors"
	"os"
	"os/exec"
)

func setExecSysProcAttr(_ *exec.Cmd) {}

func killExecGroup(pid int, _ <-chan struct{}) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

func classifyExecStartErr(err error) int {
	if errors.Is(err, os.ErrNotExist) {
		return 127
	}
	if errors.Is(err, os.ErrPermission) {
		return 126
	}
	return 125
}

func exitCodeFromWaitErr(err error) int {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 125
	}
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	return 125
}
