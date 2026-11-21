//go:build darwin

package none

import (
	"os"
	"os/exec"
)

func unwalledCommandV1(link *os.File, id string) (*exec.Cmd, *os.File, error) {
	path, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}
	helper, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	command := exec.Command(path, "wall-init", "--wall", "none", id)
	command.ExtraFiles = []*os.File{link}
	return command, helper, nil
}
