//go:build linux || darwin

package files

import (
	"os"
	"syscall"
)

func includedFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }
