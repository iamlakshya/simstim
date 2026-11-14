//go:build !linux && !darwin && !windows

package files

import (
	"fmt"
	"os"
)

func openRegularFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, fmt.Errorf("this host cannot open sandbox state without following links")
}
