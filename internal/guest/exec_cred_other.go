//go:build !linux

package guest

import (
	"fmt"
	"os"
	"os/exec"
)

func setAgentExecCredential(*exec.Cmd, []uint32) {}
func chownPathForAgent(string)                   {}
func chownFileForAgent(string)                   {}
func killAgentGroup(int)                         {}

func chownMicroVMDir(dir string, _, _ int, mode os.FileMode) error {
	//nolint:gosec // G301: caller explicitly supplies required directory mode
	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	return nil
}
