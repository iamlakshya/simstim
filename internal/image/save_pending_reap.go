package image

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"
)

// Reaping is opportunistic on the next original-domain store operation after
// a crash. A live creator's original lease is never replaced or waited out.
func (store *Store) reapSavedPending(ctx context.Context, stamp time.Time, all bool) (PurgeReport, error) {
	var report PurgeReport
	if store.domain.IsZero() {
		return report, nil
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := store.generation.CheckRoot(store.root); err != nil {
		return report, err
	}
	root, err := store.ownershipRoot()
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	defer root.Close()
	names, err := directoryEntries(root)
	if err != nil {
		return report, err
	}
	seen := map[string]bool{}
	reaped := map[string]bool{}
	for _, name := range names {
		if !pendingSaveName(name) {
			continue
		}
		if id, ok := pendingSaveFileID(name); ok && reaped[id] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		receipt, _, err := store.readSavePending(root, name)
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{"ownership/" + name, "unknown, changed or other-domain pending metadata"})
			continue
		}
		if seen[receipt.ID] {
			continue
		}
		seen[receipt.ID] = true
		if !all && stamp.Before(receipt.ExpiresAt) {
			continue
		}
		lease, available, err := store.tryExistingLease(ctx, pendingSaveLease(receipt.ID), false)
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{pendingSaveStage(receipt.ID), "the original pending lease is unavailable"})
			continue
		}
		if !available {
			if all {
				report.Left = append(report.Left, PurgeLeft{pendingSaveStage(receipt.ID), "the original prepared-image owner is still active"})
			}
			continue
		}
		remove := func() error { return store.reapSavePendingLocked(root, receipt) }
		if !store.existingOnly {
			err = store.withPublication(ctx, remove)
		} else {
			err = remove()
		}
		closeErr := lease.Close()
		err = errors.Join(err, closeErr)
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{pendingSaveStage(receipt.ID), "pending native resources changed; preserve them for inspection"})
			continue
		}
		reaped[receipt.ID] = true
		if err := store.retireSavePendingLease(receipt.ID); err != nil {
			report.Left = append(report.Left, PurgeLeft{pendingSaveLease(receipt.ID), "pending data was retired but its original lease marker remains"})
			continue
		}
		report.Removed = append(report.Removed, pendingSaveStage(receipt.ID))
	}
	return report, nil
}

func (store *Store) reapSavePendingLocked(owner *os.Root, receipt savedPendingReceipt) error {
	stage, captured := pendingSaveStage(receipt.ID), pendingSaveCaptured(receipt.ID)
	_, stageErr := store.root.Lstat(stage)
	_, capturedErr := store.root.Lstat(captured)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		return stageErr
	}
	if capturedErr != nil && !errors.Is(capturedErr, os.ErrNotExist) {
		return capturedErr
	}
	if errors.Is(stageErr, os.ErrNotExist) && errors.Is(capturedErr, os.ErrNotExist) {
		return store.finishSavePendingMetadata(owner, receipt)
	}
	if stageErr == nil && capturedErr == nil {
		return ownershipError("both pending image generations remain")
	}
	intent, _, intentErr := store.readSavePending(owner, pendingSaveExpiry(receipt.ID))
	if intentErr != nil && !errors.Is(intentErr, os.ErrNotExist) {
		return intentErr
	}
	if intentErr == nil && !reflect.DeepEqual(intent, receipt) {
		return ownershipError("the pending expiry intent differs from its original receipt")
	}
	if capturedErr == nil && errors.Is(intentErr, os.ErrNotExist) {
		return ownershipError("the captured pending directory has no original expiry intent")
	}
	if stageErr == nil {
		root, err := store.childRoot(stage)
		if err != nil {
			return err
		}
		err = store.checkSavePendingDirectory(root, receipt, false)
		root.Close()
		if err != nil {
			return err
		}
		if errors.Is(intentErr, os.ErrNotExist) {
			if _, err := store.createSavePending(owner, pendingSaveExpiry(receipt.ID), receipt); err != nil {
				return err
			}
		}
		if err := store.renameNoReplace(stage, captured); err != nil {
			return err
		}
	}
	root, err := store.childRoot(captured)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := store.checkSavePendingDirectory(root, receipt, true); err != nil {
		return err
	}
	_, liveErr := root.Lstat("rootfs.img")
	_, oldErr := root.Lstat(".expired-root")
	if liveErr == nil && oldErr == nil {
		return ownershipError("both pending file generations remain")
	}
	if liveErr == nil {
		if err := captureLeaf(root, "rootfs.img", ".expired-root", receipt.Root, 0o600); err != nil {
			return err
		}
	}
	if oldErr == nil || liveErr == nil {
		file, err := checkWitness(root, ".expired-root", receipt.Root, 0o600)
		if err != nil {
			return err
		}
		file.Close()
		if err := root.Remove(".expired-root"); err != nil {
			return err
		}
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	if err := receipt.Directory.CheckRoot(root); err != nil {
		return err
	}
	if err := store.root.Remove(captured); err != nil {
		return err
	}
	if err := store.syncDir("incoming"); err != nil {
		return err
	}
	return store.finishSavePendingMetadata(owner, receipt)
}

func (store *Store) checkSavePendingDirectory(root *os.Root, receipt savedPendingReceipt, begun bool) error {
	if err := receipt.Directory.CheckRoot(root); err != nil {
		return err
	}
	names, err := directoryEntries(root)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name != "rootfs.img" && !(begun && name == ".expired-root") {
			return ownershipError("pending image contains unreceipted children")
		}
	}
	file, err := checkWitness(root, "rootfs.img", receipt.Root, 0o600)
	if errors.Is(err, os.ErrNotExist) && begun {
		file, err = checkWitness(root, ".expired-root", receipt.Root, 0o600)
		if errors.Is(err, os.ErrNotExist) && len(names) == 0 {
			return nil
		}
	}
	if file != nil {
		file.Close()
	}
	return err
}

func (store *Store) finishSavePendingMetadata(root *os.Root, receipt savedPendingReceipt) error {
	for _, name := range []string{pendingSaveHead(receipt.ID), pendingSaveExpiry(receipt.ID), pendingSaveRetired(receipt.ID), pendingSaveExpiryRetired(receipt.ID)} {
		got, witness, err := store.readSavePending(root, name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, receipt) {
			return ownershipError("pending protocol metadata changed its original producer")
		}
		if err := removeSavePendingLeaf(root, name, receipt.ID, witness); err != nil {
			return err
		}
	}
	return nil
}
