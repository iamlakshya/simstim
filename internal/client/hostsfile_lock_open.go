package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type hostMembershipOpenExistingKey struct{}

// One native locker keeps its established create mode for canonical writers.
// Terminal retry selects an existing-only open of that SAME lock inode/path.
func openHostsMembershipLock(ctx context.Context, path string) (*os.File, func() error, func(), error) {
	if ctx.Value(hostMembershipOpenExistingKey{}) == nil {
		file, err := openLock(path, "hosts.toml")
		if err != nil {
			return nil, nil, nil, err
		}
		return file, ctx.Err, sync.OnceFunc(func() { _ = file.Close() }), nil
	}
	root, err := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil, hostStateRemovalError("terminal retry has no existing membership root")
	}
	if err != nil {
		return nil, nil, nil, err
	}
	parent, err := cachefs.CaptureRoot(root)
	if err != nil {
		_ = root.Close()
		return nil, nil, nil, err
	}
	file, err := cachefs.OpenFile(root, filepath.Base(path), os.O_RDWR, 0600)
	if err != nil {
		_ = root.Close()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil, hostStateRemovalError("terminal retry has no existing membership lock inode")
		}
		return nil, nil, nil, err
	}
	closeLock := sync.OnceFunc(func() { _ = file.Close(); _ = root.Close() })
	reference, err := cachefs.CaptureFile(root, filepath.Base(path), file, 0600)
	if err != nil {
		closeLock()
		return nil, nil, nil, err
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ambient, err := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
		if err != nil {
			return err
		}
		defer ambient.Close()
		if err := parent.CheckRoot(ambient); err != nil {
			return err
		}
		if err := parent.CheckRoot(root); err != nil {
			return err
		}
		return reference.CheckCreatedFile(root, filepath.Base(path), file, 0600)
	}
	if err := check(); err != nil {
		closeLock()
		return nil, nil, nil, err
	}
	return file, check, closeLock, nil
}

func acquireExistingHostMembershipContext(ctx context.Context) (context.Context, func(), error) {
	return acquireHostMembershipContext(context.WithValue(ctx, hostMembershipOpenExistingKey{}, struct{}{}))
}

func withExistingHostMembershipContext(ctx context.Context, run func(context.Context) error) error {
	bound, release, err := acquireExistingHostMembershipContext(ctx)
	if err != nil {
		return err
	}
	defer release()
	return run(bound)
}
