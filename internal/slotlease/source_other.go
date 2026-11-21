//go:build !linux

package slotlease

import (
	"context"
	"fmt"
	"os"
)

// SourceV1 has no native authority on a platform without Linux cgroup2.
type SourceV1 struct{}

func unsupported() error {
	return fmt.Errorf("slot leases need Linux namespaces and cgroup2\nTry: prepare a Linux host for separate accounts.")
}
func CaptureKeeperV1() (*SourceV1, error)              { return nil, unsupported() }
func (*SourceV1) CheckCurrentV1(context.Context) error { return unsupported() }
func (*SourceV1) RetainForInitV1() (*os.File, error)   { return nil, unsupported() }
func (*SourceV1) Close() error                         { return nil }
