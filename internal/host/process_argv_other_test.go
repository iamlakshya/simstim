//go:build !darwin

package host

import (
	"os"
	"os/exec"
	"strconv"
)

// ownedProcessArgs keeps the original ps inspection on other platforms.
func ownedProcessArgs(process *os.Process) ([]byte, error) {
	return exec.Command("ps", "-ww", "-o", "args=", "-p", strconv.Itoa(process.Pid)).Output()
}
