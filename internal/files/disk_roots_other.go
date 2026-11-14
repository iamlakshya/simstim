//go:build !linux

package files

import (
	"fmt"
	"os"
)

// ValidateDiskRoots refuses accounting outside the Linux sandbox-host platform.
func ValidateDiskRoots(*os.Root, ...*os.Root) error {
	return fmt.Errorf("owned disk mount validation requires the Linux host helper")
}
