//go:build linux

package chv

import (
	"context"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/wall"
	"github.com/iamlakshya/simstim/internal/wall/microvm"
)

// Options binds the existing host-owned resource and publication dependencies.
type Options = microvm.Options

// Driver is the shared engine configured for Cloud Hypervisor.
type Driver = microvm.Driver

// DirectResult is the controlled guest direct-I/O evidence record.
type DirectResult = microvm.DirectResult

// DiskInfo describes the existing persistent VM disk.
type DiskInfo = microvm.DiskInfo

// DiskFormatter formats a newly created disk, never an existing one.
type DiskFormatter = microvm.DiskFormatter

// SessionDiskName is the shared session disk's layout name.
const SessionDiskName = microvm.SessionDiskName

// New constructs the existing backend without starting any process.
func New(options Options) (*Driver, error) { return microvm.New(options) }

// CurrentMemoryBoundary checks the actual admitted unit's memory boundary.
func CurrentMemoryBoundary(ctx context.Context, budget MemoryBudget) error {
	return microvm.CurrentMemoryBoundary(ctx, budget)
}

// EnsureDisk preserves the shared atomic disk creation and ownership rules.
func EnsureDisk(ctx context.Context, dir string, size int64, format DiskFormatter) (DiskInfo, error) {
	return microvm.EnsureDisk(ctx, dir, size, format)
}

// FormatDisk runs the canonical confined formatter on a fresh disk descriptor.
func FormatDisk(ctx context.Context, mkfs string, output *os.File) error {
	return microvm.FormatDisk(ctx, mkfs, output)
}

// RecordDirectResult records only the existing controlled test result.
func RecordDirectResult(ctx context.Context, spec wall.Spec, passed bool) error {
	return microvm.RecordDirectResult(ctx, spec, passed)
}

// RunDiskHelper serves the existing closed disk-format helper role.
func RunDiskHelper(in io.Reader, out io.Writer) error { return microvm.RunDiskHelper(in, out) }

// RunVMHelper serves the existing owned VMM runner role.
func RunVMHelper(in io.Reader) error { return microvm.RunVMHelper(in) }

// RunVMMExec serves the existing pinned external-exec role.
func RunVMMExec(in io.Reader) error { return microvm.RunVMMExec(in) }
