//go:build !linux && !darwin && !windows

package files

import (
	"fmt"
	"os"
)

// Identity returns the opened object's opaque filesystem identity.
func Identity(_ *os.File) (string, error) {
	return "", fmt.Errorf("this host has no mount-source identity support")
}
