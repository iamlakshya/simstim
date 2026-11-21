//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// sudo forwards SIGTERM to its original child. Wait retains that exact owned
// process until it exits; a cancelled context cannot manufacture completion.
func configureJoinSudo(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 0
}
