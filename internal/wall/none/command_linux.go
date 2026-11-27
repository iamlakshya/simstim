//go:build linux

package none

import (
	"os"
	"os/exec"
)

func unwalledCommandV1(link *os.File, id string) (*exec.Cmd, *os.File, error) {
	helper, err := os.Open("/proc/self/exe")
	if err != nil {
		return nil, nil, err
	}
	command := exec.Command("/proc/self/fd/4", "wall-init", "--wall", "none", id)
	command.ExtraFiles = []*os.File{link, helper}
	return command, helper, nil
}
