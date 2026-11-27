//go:build !windows

package client

import (
	"fmt"
	"os"
	"syscall"
)

func terminateGitProcess(process *os.Process) error {
	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop the command: %w", err)
	}
	return nil
}
