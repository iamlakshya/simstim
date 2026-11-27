//go:build !linux

package process

import (
	"context"
	"os"
)

// WithForkRootsV1 needs the original native process launcher on Linux.
func (owner *CopyOwnerV1) WithForkRootsV1(context.Context, func(work, home *os.Root) error) error {
	return setupError("the original process fork roots need Linux", nil)
}
