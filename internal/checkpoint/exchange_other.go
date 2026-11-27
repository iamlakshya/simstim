//go:build !linux

package checkpoint

import (
	"fmt"
	"os"
)

func exchangeFilesV1(_ *os.Root, _, _ string) (bool, error) {
	return false, fmt.Errorf("atomic stopped-disk role exchange needs the native Linux runtime")
}
