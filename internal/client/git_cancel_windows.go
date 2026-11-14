//go:build windows

package client

import (
	"fmt"
	"os"
)

func terminateGitProcess(process *os.Process) error {
	if err := process.Kill(); err != nil {
		return fmt.Errorf("stop the command: %w", err)
	}
	return nil
}
