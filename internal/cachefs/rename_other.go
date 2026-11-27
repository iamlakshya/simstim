//go:build !linux && !darwin && !windows

package cachefs

import (
	"fmt"
	"os"
)

func renameBetweenNoReplaceNative(_ *os.Root, _ string, _ *os.Root, _ string) error {
	return fmt.Errorf("this platform has no native no-clobber cache rename")
}
