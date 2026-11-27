//go:build !linux

package files

import (
	"context"
	"fmt"
	"os"
)

// MeasureUsage refuses use outside the Linux sandbox-host platform. Supported
// desktop clients can still query SpaceAt without this host-only operation.
func MeasureUsage(context.Context, ...*os.Root) (Usage, error) {
	return Usage{}, fmt.Errorf("allocated session accounting requires the Linux host helper")
}
