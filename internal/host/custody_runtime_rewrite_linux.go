//go:build linux

package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// rewriteCustodyRuntimeV1 replaces only the same descriptor-owned runtime
// service under the per-user configuration lock. An upgraded helper gets a
// real new ExecStart before StartUnit; no immutable transient unit is reused.
func (manager *nativeUserManagerV1) rewriteCustodyRuntimeV1(ctx context.Context, runtime *custodyRuntimeV1, m *Manifest, executable string, argv []string) (failure error) {
	if runtime == nil || runtime.service == nil || runtime.pin == nil {
		return sessionLeaseError("reactivation lost its owned runtime configuration")
	}
	content, err := custodyServiceFileV1(m, executable, argv)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	config, err := manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, config.Close()) }()
	if err := errors.Join(config.rootRef.CheckRoot(runtime.root), runtime.service.check(), runtime.pin.check(), manager.checkCustodyPinV1(ctx, UnitName(m.ID))); err != nil {
		return err
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return err
	}
	stem := ".custody-" + hex.EncodeToString(entropy[:])
	next, err := openCustodyConfigFileV1(runtime.root, stem+".part", content, true)
	if err != nil {
		if next != nil {
			runtime.retiredFiles = append(runtime.retiredFiles, &custodyRetiredConfigV1{file: next})
		}
		return err
	}
	temporary := &custodyRetiredConfigV1{file: next}
	runtime.retiredFiles = append(runtime.retiredFiles, temporary)
	published := false
	defer func() {
		if !published {
			failure = errors.Join(failure, temporary.closeV1())
		}
	}()
	old := runtime.service
	if err := cachefs.RenameOwnedFileNoReplace(runtime.root, old.file, old.name, stem+".old", 0o600); err != nil {
		return err
	}
	old.name = stem + ".old"
	retired := &custodyRetiredConfigV1{file: old}
	runtime.retiredFiles = append(runtime.retiredFiles, retired)
	if err := old.check(); err != nil {
		return err
	}
	if err := cachefs.RenameOwnedFileNoReplace(runtime.root, next.file, next.name, UnitName(m.ID), 0o600); err != nil {
		// Restore only our captured original inode, and only into an absent
		// canonical name. A substitute at that name is always preserved.
		restore := cachefs.RenameOwnedFileNoReplace(runtime.root, old.file, old.name, UnitName(m.ID), 0o600)
		if restore == nil {
			old.name = UnitName(m.ID)
			retired.file = nil // The original inode is current again.
		}
		return errors.Join(err, restore)
	}
	next.name = UnitName(m.ID)
	runtime.service = next
	temporary.file = nil // The actual new inode is now the current service.
	published = true
	if err := errors.Join(next.check(), config.reload(ctx)); err != nil {
		return err
	}
	if err := retired.closeV1(); err != nil {
		return err
	}
	// This is durable runtime configuration, not evidence of inherited
	// custody. Only the genuine new main can validate the retained 5 FDs.
	directory, err := runtime.root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(cachefs.SyncDirectory(directory), directory.Close(), config.check(ctx), manager.checkCustodyPinV1(ctx, UnitName(m.ID)))
}

func (runtime *custodyRuntimeV1) retireConfigV1(ctx context.Context, manager *nativeUserManagerV1) (failure error) {
	if runtime == nil || runtime.service == nil || runtime.pin == nil {
		return sessionLeaseError("native retirement lost its original configuration descriptors")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	config, err := manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return err
	}
	defer func() { failure = errors.Join(failure, config.Close()) }()
	if err := config.rootRef.CheckRoot(runtime.root); err != nil {
		return err
	}
	if !runtime.pinRemoved {
		if err := runtime.pin.remove(); err != nil {
			return err
		}
		runtime.pinRemoved = true
	}
	if !runtime.serviceRemoved {
		if err := runtime.service.remove(); err != nil {
			return err
		}
		runtime.serviceRemoved = true
	}
	if err := errors.Join(syncCapturedDirectory(runtime.root), syncCapturedDirectory(runtime.pins)); err != nil {
		return err
	}
	if err := config.reload(ctx); err != nil {
		return err
	}
	runtime.retired = true
	return nil
}
