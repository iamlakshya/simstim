//go:build linux

package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/wall"
)

// prepareGVisor consumes the original native loan and the ONE process
// filesystem plan. The host constructor fixes the runtime issuer, storage
// domain and confined command owner; no caller callback or inventory is used.
func prepareGVisor(ctx context.Context, m *host.Manifest) (_ sandbox, returned error) {
	expected, err := originalGvisorPlan(ctx, m)
	if err != nil {
		return nil, err
	}
	receipt := host.OriginalVMNeedFromContext(ctx)
	spec, capture, err := prepareProcessFilesystem(m, false)
	if err != nil {
		return nil, err
	}
	prepared := false
	defer func() {
		if !prepared && capture != nil {
			returned = errors.Join(returned, capture.Close())
		}
	}()
	canonical, err := filepath.EvalSymlinks(expected.SessionDir)
	if err != nil || canonical != spec.SessionDir || !slices.Equal(expected.StateDirs, spec.StateDirs) || !slices.Equal(expected.StateFiles, spec.StateFiles) {
		return nil, errors.Join(execRefusal("the gvisor filesystem differs from its original protected plan"), err)
	}
	if host.OriginalVMNeedFromContext(ctx) != receipt {
		return nil, execRefusal("the original gvisor operation ended during filesystem preparation")
	}
	spec.RuntimeManifest = expected.RuntimeManifest
	// This changes only derived filesystem plan data. The same private
	// accepted native receipt remains in ctx and is checked again by the host.
	owner := wall.WithExpectedSpec(ctx, spec)
	driver, err := host.NewGvisorDriver()
	if err != nil {
		return nil, err
	}
	disk, err := processDiskResourcesV1(spec)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !prepared {
			returned = errors.Join(returned, disk.Close())
		}
	}()
	runtime, err := prepareGvisorLayout(owner, driver, spec)
	if err != nil {
		return nil, err
	}
	release, ok := runtime.(interface{ ReleasePreparedV1() error })
	if !ok {
		return nil, execRefusal("the gvisor factory returned no actual prepared-resource owner")
	}
	// Gvisor's actual runtime lock belongs to this same factory. Its release
	// refuses a live or uncertain init. Process metadata-only Close is unchanged.
	disk.release = release.ReleasePreparedV1
	if host.OriginalVMNeedFromContext(ctx) != receipt {
		return nil, execRefusal("the original gvisor operation ended before runtime preparation completed")
	}
	prepared = true
	return processSandbox{Sandbox: runtime, capture: capture, disk: disk}, nil
}

// Existing layout chooses Open, never execution pins. Both paths receive the
// same original native context and newly derived filesystem projection.
func prepareGvisorLayout(ctx context.Context, driver wall.Driver, spec wall.Spec) (wall.Sandbox, error) {
	root, err := cachefs.OpenPrivateRoot(spec.SessionDir, false)
	if err != nil {
		return nil, err
	}
	info, inspectErr := root.Lstat("gvisor")
	closeErr := root.Close()
	if closeErr != nil {
		return nil, errors.Join(inspectErr, closeErr)
	}
	if errors.Is(inspectErr, os.ErrNotExist) {
		return driver.Prepare(ctx, spec)
	}
	if inspectErr != nil {
		return nil, inspectErr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, execRefusal("the saved gvisor layout is not a private runtime directory")
	}
	return driver.Open(ctx, spec.SessionDir)
}
