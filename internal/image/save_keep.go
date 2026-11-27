package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"time"
)

// Keep publishes only this reviewed creator's root. Alias and platform
// leases cover local publication, never the client's review or root stream.
func (prepared *PreparedSave) Keep(ctx context.Context) (result SaveResult, returned error) {
	if prepared == nil || ctx == nil || prepared.owner == nil {
		return result, saveProtocol("image keep has no original pending owner")
	}
	owned, cancel := context.WithDeadline(ctx, prepared.deadline)
	defer cancel()
	done := make(chan struct{})
	stop := context.AfterFunc(prepared.owner, func() { defer close(done); cancel() })
	defer func() {
		if !stop() {
			<-done
		}
	}()
	ctx = owned
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if prepared.kept {
		return SaveResult{Image: cloneSavedRecord(prepared.record), HostID: prepared.store.domain.HostID}, nil
	}
	if prepared.closed || !time.Now().Before(prepared.deadline) {
		return result, saveFailure("image_expired", "the original saved-image review has ended")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	store := prepared.store
	alias := sha256.Sum256([]byte(prepared.record.Ref))
	aliasLease, err := store.lock(ctx, "saved-"+hex.EncodeToString(alias[:])+".lock", false)
	if err != nil {
		return result, err
	}
	defer aliasLease.Close()
	// No platform lease is held while inventory takes its short shared leases.
	if !prepared.replace {
		records, err := store.List(ctx)
		if err != nil {
			return result, err
		}
		for _, existing := range records {
			if existing.SavedHost != nil && slices.Contains(existing.SavedAliases, prepared.record.Ref) {
				return result, saveFailure("image_exists", "the original host already has an image with this name")
			}
		}
	}
	lease, err := store.lock(ctx, prepared.record.Digest[7:]+".lock", false)
	if err != nil {
		return result, err
	}
	defer lease.Close()
	if err := prepared.checkRoot(ctx); err != nil {
		return result, err
	}
	if existing, err := store.readRecord(prepared.record.Digest[7:]); err == nil {
		if existing.SavedHost == nil || existing.SavedHost.HostID != store.domain.HostID || existing.Arch != prepared.record.Arch || existing.RootHash != prepared.record.RootHash || existing.DataBlocks != prepared.record.DataBlocks {
			return result, saveProtocol("the same digest has a different native image producer")
		}
		if err := prepared.addAlias(ctx, &existing); err != nil {
			return result, err
		}
		request := prepared.request()
		projected, err := projectSavedSelection(request, existing)
		if err != nil {
			return result, err
		}
		prepared.record = projected
		// This private duplicate is still this creator's uncommitted object.
		if err := prepared.cleanupDuplicate(); err != nil {
			return result, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	} else {
		if err := prepared.publishRoot(ctx); err != nil {
			return result, err
		}
	}
	prepared.kept = true
	result = SaveResult{Image: cloneSavedRecord(prepared.record), HostID: store.domain.HostID}
	if err := result.Validate(prepared.record.Ref, store.domain.HostID); err != nil {
		return SaveResult{}, err
	}
	return result, nil
}

func cloneSavedRecord(record Record) Record {
	record.SavedHost = CloneSavedHostV1(record.SavedHost)
	record.SavedAliases = slices.Clone(record.SavedAliases)
	return record
}

func (prepared *PreparedSave) request() Request {
	record := prepared.record
	return Request{Ref: record.Ref, Digest: record.Digest, PlatformDigest: record.PlatformDigest, Arch: record.Arch, RootHash: record.RootHash, DataBlocks: record.DataBlocks, SavedHost: CloneSavedHostV1(record.SavedHost)}
}

func (prepared *PreparedSave) checkRoot(ctx context.Context) error {
	if err := prepared.directory.CheckRoot(prepared.root); err != nil {
		return err
	}
	if err := prepared.creator.File.CheckCreatedFile(prepared.root, "rootfs.img", prepared.output, 0o600); err != nil {
		return err
	}
	digest, err := savedRootDigest(ctx, prepared.output)
	if err != nil {
		return err
	}
	actual, err := Inspect(prepared.output)
	if err != nil {
		return err
	}
	if digest != prepared.record.Digest || actual.RootHash != prepared.record.RootHash || actual.DataBlocks != prepared.record.DataBlocks || actual.Size != prepared.record.Size {
		return mismatch("the reviewed original pending root")
	}
	return prepared.creator.File.CheckCreatedFile(prepared.root, "rootfs.img", prepared.output, 0o600)
}

func (prepared *PreparedSave) addAlias(ctx context.Context, existing *Record) error {
	store := prepared.store
	receipt, err := store.loadOwnership(ctx, existing.Digest)
	if err != nil {
		return err
	}
	root, err := store.regular(existing.Digest[7:]+"/rootfs.img", os.O_RDONLY, 0o444)
	if err != nil {
		return err
	}
	checked := store.checkSavedDescriptor(ctx, *existing, root)
	if err := errors.Join(checked, root.Close()); err != nil {
		return err
	}
	if slices.Contains(existing.SavedAliases, prepared.record.Ref) {
		return nil
	}
	existing.SavedAliases = append(slices.Clone(existing.SavedAliases), prepared.record.Ref)
	slices.Sort(existing.SavedAliases)
	return store.replaceOwnedMetadata(ctx, &receipt, *existing)
}

func (prepared *PreparedSave) cleanupDuplicate() error {
	if err := prepared.store.cleanupStage(prepared.stage, prepared.root, prepared.directory, map[string]createdImageFile{"rootfs.img": prepared.creator}); err != nil {
		return err
	}
	prepared.durable = true // no pending namespace remains to unlink later
	return nil
}

func (prepared *PreparedSave) publishRoot(ctx context.Context) error {
	store := prepared.store
	if err := prepared.output.Chmod(0o444); err != nil {
		return err
	}
	if err := prepared.output.Sync(); err != nil {
		return err
	}
	metadata, err := store.saveFreshRecord(ctx, prepared.stage, prepared.record)
	if err != nil {
		return err
	}
	file, err := checkWitness(prepared.root, "image.json", metadata, 0o600)
	if err != nil {
		return err
	}
	created, err := store.captureCreated(prepared.stage+"/image.json", file, 0o600)
	if err != nil {
		file.Close()
		return err
	}
	prepared.metadata = &created
	disk, err := witnessFile(prepared.root, "rootfs.img", prepared.output, 0o444, false)
	if err != nil {
		return err
	}
	if disk.Ref.Identity != prepared.creator.File.Identity {
		return ownershipError("the reviewed root changed its native creator")
	}
	if err := syncRoot(prepared.root); err != nil {
		return err
	}
	if err := store.syncDir("incoming"); err != nil {
		return err
	}
	receipt := imageOwnership{Version: 1, Domain: store.receiptDomain(), Digest: prepared.record.Digest, Store: store.generation, Directory: prepared.directory, Leaf: prepared.stage, Disk: disk, Metadata: metadata}
	// Once a publication intent may be durable, only its existing replay owner
	// may finish it. Ordinary Close never unlinks that protected intent.
	prepared.durable = true
	if err := store.publishPrepared(ctx, receipt); err != nil {
		return err
	}
	return prepared.output.Sync()
}
