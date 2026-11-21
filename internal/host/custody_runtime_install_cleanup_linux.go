//go:build linux

package host

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// An exclusive original creation owns its incomplete bytes as well. This
// method never treats a substitute name or a loaded unit as cleanup authority.
func (file *custodyConfigFileV1) removeCreatedV1() error {
	if file == nil || file.file == nil || !file.created {
		return sessionLeaseError("partial runtime cleanup lost its actual exclusive creation")
	}
	var native unix.Stat_t
	if err := unix.Fstat(int(file.file.Fd()), &native); err != nil {
		return err
	}
	if native.Nlink == 0 {
		return nil
	}
	if _, err := cachefs.ValidateFile(file.root, file.name, file.file, 0o600); err != nil {
		return err
	}
	return file.root.Remove(file.name)
}

func (runtime *custodyRuntimeV1) cleanupPendingInstallLockedV1(ctx context.Context) (failure error) {
	if runtime.installManager == nil {
		return sessionLeaseError("the partial runtime installation lost its original manager")
	}
	config, err := runtime.installManager.lockCustodyConfigV1(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, config.Close()) }()
	return runtime.cleanupPendingInstallWithConfigLockedV1(ctx, config)
}

// The install failure path calls this under its already-held SAME config
// scope. Reopening the OFD lock while holding it would deadlock cleanup.
func (runtime *custodyRuntimeV1) cleanupPendingInstallWithConfigLockedV1(ctx context.Context, config *custodyConfigV1) error {
	if !runtime.pendingInstall || config == nil || config.manager != runtime.installManager {
		return sessionLeaseError("partial runtime cleanup has another original configuration owner")
	}
	if err := config.check(ctx); err != nil {
		return err
	}
	if runtime.root != nil {
		if err := config.rootRef.CheckRoot(runtime.root); err != nil {
			return err
		}
	}
	if runtime.anchor != nil && runtime.anchor.incomplete {
		if err := runtime.anchor.removeCreatedV1(); err != nil {
			return err
		}
		if err := syncCapturedDirectory(runtime.anchor.root); err != nil {
			return err
		}
	}
	for _, entry := range []struct {
		file    *custodyConfigFileV1
		removed *bool
	}{{runtime.pin, &runtime.pinRemoved}, {runtime.service, &runtime.serviceRemoved}} {
		if entry.file == nil || *entry.removed {
			continue
		}
		if err := entry.file.removeCreatedV1(); err != nil {
			return err
		}
		*entry.removed = true
		if err := syncCapturedDirectory(entry.file.root); err != nil {
			return err
		}
	}
	if err := config.reload(ctx); err != nil {
		return err
	}
	runtime.pendingInstall = false
	return nil
}
