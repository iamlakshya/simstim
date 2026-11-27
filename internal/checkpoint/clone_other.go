//go:build !linux

package checkpoint

import (
	"errors"
	"os"
)

var errCloneUnsupported = errors.New("FICLONE is unavailable on this host")

func cloneFile(_, _ *os.File) error { return errCloneUnsupported }

func cloneUnsupported(err error) bool { return errors.Is(err, errCloneUnsupported) }
