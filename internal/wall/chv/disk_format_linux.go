//go:build linux

package chv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// FormatDisk invokes the parent-selected host formatter from inside the
// preparation jail. Its binary, loader, libraries and configuration must be
// read-only binds of that jail. It is never an unjailed fallback.
func FormatDisk(ctx context.Context, mkfsPath string, output *os.File) error {
	command, err := diskFormatCommand(ctx, mkfsPath, output)
	if err != nil {
		return err
	}
	var diagnostic diskDiagnostic
	command.Stdout = &diagnostic
	command.Stderr = &diagnostic
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("run host ext4 formatter: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func diskFormatCommand(ctx context.Context, mkfsPath string, output *os.File) (*exec.Cmd, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(mkfsPath) || filepath.Clean(mkfsPath) != mkfsPath || strings.ContainsRune(mkfsPath, 0) {
		return nil, fmt.Errorf("the host ext4 formatter needs an absolute clean executable path")
	}
	if _, err := cachefs.ValidateDescriptor(output, 0o600); err != nil {
		return nil, fmt.Errorf("check fresh session disk formatter descriptor: %w", err)
	}
	command := exec.CommandContext(ctx, mkfsPath, "-q", "-F", "-E", "root_owner=1000:1000", "/proc/self/fd/3")
	// mke2fs selects the filesystem type from argv0. A descriptor path or a
	// resolved mke2fs alias must not silently select another filesystem type.
	command.Args[0] = "mkfs.ext4"
	command.ExtraFiles = []*os.File{output}
	command.Env = []string{"LC_ALL=C", "LANG=C"}
	command.WaitDelay = 5 * time.Second
	return command, nil
}

// diskDiagnostic retains the most recent 64 KiB without blocking a formatter
// whose error output is larger. exec joins this one stream before it is read.
type diskDiagnostic struct{ data []byte }

func (d *diskDiagnostic) Write(p []byte) (int, error) {
	const limit = 64 << 10
	n := len(p)
	if len(p) >= limit {
		d.data = append(d.data[:0], p[len(p)-limit:]...)
		return n, nil
	}
	drop := len(d.data) + len(p) - limit
	if drop > 0 {
		copy(d.data, d.data[drop:])
		d.data = d.data[:len(d.data)-drop]
	}
	d.data = append(d.data, p...)
	return n, nil
}
func (d *diskDiagnostic) String() string { return string(d.data) }
