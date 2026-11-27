//go:build darwin

package cli

import "golang.org/x/sys/unix"

// ioctlGetTermios reads a terminal's modes.
const ioctlGetTermios = unix.TIOCGETA
