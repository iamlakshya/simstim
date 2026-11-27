//go:build linux || darwin

package files

import (
	"fmt"
	"os"
	"syscall"
)

// Identity returns the opened object's opaque filesystem identity.
func Identity(f *os.File) (string, error) {
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	raw, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("mount source has no filesystem identity")
	}
	return fmt.Sprintf("%x:%x", uint64(raw.Dev), uint64(raw.Ino)), nil
}
