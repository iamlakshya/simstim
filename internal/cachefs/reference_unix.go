//go:build unix

package cachefs

import (
	"fmt"
	"os"
	"syscall"
)

func referenceIdentity(_ *os.File, info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("native file identity is unavailable")
	}
	return fmt.Sprintf("unix:%v:%v:%v", stat.Dev, stat.Ino, stat.Uid), nil
}
