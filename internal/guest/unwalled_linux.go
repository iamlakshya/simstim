//go:build linux

package guest

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func unwalledProcessAttributesV1() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func adoptUnwalledExecutableV1() (*os.File, error) {
	file := os.NewFile(4, "original-unwalled-executable")
	if file == nil {
		return nil, execError("the unwalled init has no original held executable")
	}
	unix.CloseOnExec(4)
	actual, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	mapped, err := os.Stat("/proc/self/exe")
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(actual, mapped) {
		file.Close()
		return nil, execError("the unwalled executable descriptor is not its actual mapped helper")
	}
	return file, nil
}
