//go:build linux

package host

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (purge *purgeOwnerV1) destroyOrdinarySessionResourcesV1(operation *SessionOperation, stopped *StoppedOrdinarySessionV1) error {
	if purge == nil || operation == nil || operation.native == nil || stopped == nil || stopped.origin != purge.placement {
		return sessionLeaseError("ordinary cleanup has no same original purge and native owner")
	}
	owner := &stoppedCleanupOwnerV1{purge: purge, operation: operation, ordinary: stopped, root: operation.native.root, id: operation.Primary(), leaseNames: map[string]bool{}}
	if err := owner.check(purge.ctx); err != nil {
		return err
	}
	manifest, err := operation.LoadContext(purge.ctx)
	if err != nil {
		return err
	}
	if manifest.Wall != nil || manifest.SandboxOf != "" || manifest.Auxiliary != nil || len(manifest.VMNeedV1) != 0 || len(manifest.extra) != 0 {
		return sessionLeaseError("the ordinary stopped source has another or unknown resource role")
	}
	members, err := SandboxMembers(manifest.ID)
	if err != nil {
		return err
	}
	if len(members) != 0 {
		return sessionLeaseError("an ordinary stopped source cannot settle sandbox member storage")
	}
	directory, err := owner.root.Open(".")
	if err != nil {
		return err
	}
	owner.mount, err = stoppedCleanupMountV1(directory)
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	defer func() {
		for _, lease := range owner.leases {
			_ = lease.Close()
		}
	}()
	entries, err := stoppedCleanupEntriesV1(owner.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "manifest.json" || name == lockFile || name == firstLockFile || name == holderLockFile {
			continue
		}
		if !knownStoppedOrdinaryResourceV1(name) {
			return sessionLeaseError("the ordinary root retains an unknown resource: " + name)
		}
		if err := owner.removeResource(purge.ctx, name, 0); err != nil {
			return err
		}
	}
	// The actual ordinary producer requires its original socket closure. This
	// consumer never adopts a residual same-name socket as the original one.
	return owner.check(purge.ctx)
}

func knownStoppedOrdinaryResourceV1(name string) bool {
	switch name {
	case "holder.log", "holder.log.1", "debug.jsonl", "debug.jsonl.1", "audit.jsonl", "audit.jsonl.1", "events.jsonl", "events.jsonl.1", "rename.unsaved", "paste", "pull", "debug", "inputs":
		return true
	}
	return false
}

func (purge *purgeOwnerV1) finishOrdinarySessionMetadataV1(operation *SessionOperation, stopped *StoppedOrdinarySessionV1) error {
	if purge == nil || operation == nil || operation.native == nil || stopped == nil || stopped.holder == nil || stopped.origin != purge.placement {
		return sessionLeaseError("ordinary terminal cleanup lost its same original native holder")
	}
	ctx := purge.ctx
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	root := operation.native.root
	entries, err := stoppedCleanupEntriesV1(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "manifest.json" && name != lockFile && name != firstLockFile && name != holderLockFile {
			return sessionLeaseError("the ordinary root retains an unsettled resource: " + name)
		}
	}
	first, err := cachefs.OpenFile(root, firstLockFile, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer first.Close()
	held, err := cachefs.TryLock(first, cachefs.Exclusive)
	if err != nil || !held {
		return sessionLeaseError("the original ordinary first-save owner has not settled")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	directoryInfo, err := directory.Stat()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	manifest, err := cachefs.OpenFile(root, "manifest.json", os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	manifestInfo, err := cachefs.ValidateFile(root, "manifest.json", manifest, 0o600)
	closeErr = manifest.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	if err := purge.removeFile("state", filepath.Join("sessions", operation.Primary(), "manifest.json"), refOf(manifestInfo), func(*os.File) error { return stopped.Check(ctx, operation) }); err != nil {
		return err
	}
	// No metadata reload follows the last original manifest unlink. The SAME
	// concrete ordinary source continues to prove its actual retained objects.
	native := func() error {
		return errors.Join(ctx.Err(), stopped.checkStoppedOrdinaryNativeFactsV1(ctx), operation.native.rootRef.CheckRoot(root))
	}
	terminal := []struct {
		name string
		file *os.File
	}{{firstLockFile, first}, {holderLockFile, stopped.holder.file}, {lockFile, operation.native.file}}
	for _, entry := range terminal {
		if err := native(); err != nil {
			return err
		}
		info, err := cachefs.ValidateFile(root, entry.name, entry.file, 0o600)
		if err != nil || info.Size() != 0 {
			return sessionLeaseError("the original ordinary terminal lease changed its native role")
		}
		capture := ".ordinary-final-" + purge.args.Nonce + "-" + entry.name
		if err := removeCapturedFile(root, entry.name, refOf(info), capture, func(string) error { return native() }, nil); err != nil {
			return err
		}
	}
	if err := native(); err != nil {
		return err
	}
	return purge.removeEmptyDirectory(operation.native.path, refOf(directoryInfo))
}
