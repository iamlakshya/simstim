//go:build !linux

package guest

import (
	"os"
	"syscall"
)

func unwalledProcessAttributesV1() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

func adoptUnwalledExecutableV1() (*os.File, error) { return nil, nil }
