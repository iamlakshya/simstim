package image

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// PurgeLeft reports a store-relative name that lacks current cleanup authority.
// It is never permission for another layer to remove that path.
type PurgeLeft struct{ Path, Reason string }

// PurgeReport distinguishes completed owned cleanup from preserved resources.
// Empty infrastructure directories remain for the caller's exact-root rmdir.
type PurgeReport struct {
	Removed []string
	Left    []PurgeLeft
}

// PurgeOwned removes every receipt-owned image, regardless of defaults or age.
// The host first drains producers and retains its external operation guard.
// Each image uses its existing exclusive lease and fresh trusted references.
// Legacy pairs, unknown parts and foreign entries stay reported. No new lock
// domain, normal retention prune or recursive deletion is used.
func (s *Store) PurgeOwned(ctx context.Context, refs References) (PurgeReport, error) {
	var report PurgeReport
	if refs == nil {
		return report, ownershipError("trusted image references are unavailable")
	}
	if err := s.generation.CheckRoot(s.root); err != nil {
		return report, err
	}
	pending, err := s.reapSavedPending(ctx, time.Now().UTC(), true)
	if err != nil {
		return report, err
	}
	report.Removed = append(report.Removed, pending.Removed...)
	report.Left = append(report.Left, pending.Left...)
	names, err := directoryEntries(s.root)
	if err != nil {
		return report, err
	}
	platforms := map[string]bool{}
	for _, name := range names {
		if ValidDigest("sha256:" + name) {
			platforms["sha256:"+name] = true
		}
		if strings.HasSuffix(name, ".lock") && ValidDigest("sha256:"+strings.TrimSuffix(name, ".lock")) {
			platforms["sha256:"+strings.TrimSuffix(name, ".lock")] = true
		}
	}
	ownerRoot, err := s.ownershipRoot()
	if errors.Is(err, os.ErrNotExist) {
		s.inventoryUnowned(&report)
		return report, nil
	}
	if err != nil {
		return report, err
	}
	receipts, err := directoryEntries(ownerRoot)
	ownerRoot.Close()
	if err != nil {
		return report, err
	}
	if err := s.recoverLeaseHeads(ctx, &report); err != nil {
		return report, err
	}
	for _, name := range receipts {
		if strings.HasPrefix(name, "image-") && strings.HasSuffix(name, ".json") {
			digest := "sha256:" + strings.TrimSuffix(strings.TrimPrefix(name, "image-"), ".json")
			if ValidDigest(digest) {
				platforms[digest] = true
			}
		}
	}
	ordered := make([]string, 0, len(platforms))
	for digest := range platforms {
		ordered = append(ordered, digest)
	}
	sort.Strings(ordered)
	for _, digest := range ordered {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		lease, err := s.purgePlatformLease(ctx, digest[7:]+".lock")
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{digest[7:], "the existing platform lease is missing, unsafe or has another domain: " + err.Error()})
			if ctx.Err() != nil {
				return report, ctx.Err()
			}
			continue
		}
		used, err := refs()
		if err != nil {
			lease.Close()
			return report, err
		}
		if used[digest] != "" {
			report.Left = append(report.Left, PurgeLeft{digest[7:], "a trusted session still references this image"})
			lease.Close()
			continue
		}
		err = s.purgeImageLocked(ctx, digest, &report)
		lease.Close()
		if err != nil {
			report.Left = append(report.Left, PurgeLeft{digest[7:], err.Error()})
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	// Image authority failures are not permission to remove their lock domain.
	if len(report.Left) == 0 {
		if err := s.purgeOwnedLeases(ctx, &report); err != nil {
			return report, err
		}
	}
	s.recoverMovedReceipts(&report)
	s.inventoryUnowned(&report)
	return report, nil
}

func (s *Store) purgeImageLocked(ctx context.Context, digest string, report *PurgeReport) error {
	receipt, err := s.loadOwnership(ctx, digest)
	if errors.Is(err, os.ErrNotExist) {
		if _, err := s.root.Lstat(digest[7:]); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return ownershipError("legacy image has no current producer receipt")
	}
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source := receipt.Leaf
	if stageLeaf(source) {
		if _, err := s.root.Lstat(digest[7:]); err == nil {
			root, err := s.ownedDirectory(receipt, digest[7:], false)
			if err != nil {
				return ownershipError("prepared publication collides with an unowned destination")
			}
			root.Close()
			source = digest[7:]
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	begun := receipt.Purge != nil
	if !begun {
		if _, err := s.root.Lstat(source); errors.Is(err, os.ErrNotExist) {
			return s.removeImageReceipt(receipt)
		}
		root, err := s.ownedDirectory(receipt, source, false)
		if err != nil {
			return err
		}
		root.Close()
		quarantine, err := randomLeaf(".image-purge-")
		if err != nil {
			return err
		}
		receipt.Leaf = source
		receipt.Purge = &purgeIntent{Quarantine: quarantine}
		if err := s.writeOwnership(receipt); err != nil {
			return err
		}
	}
	quarantine := receipt.Purge.Quarantine
	captured, openErr := s.childRoot(quarantine)
	if errors.Is(openErr, os.ErrNotExist) {
		if _, err := s.root.Lstat(source); errors.Is(err, os.ErrNotExist) {
			return s.removeImageReceipt(receipt)
		}
		before, err := s.ownedDirectory(receipt, source, begun)
		if err != nil {
			return err
		}
		before.Close()
		if s.captureHook != nil {
			s.captureHook(true, s.root, source, quarantine)
		}
		if err := cachefs.RenameNoReplace(s.root, source, quarantine); err != nil {
			return err
		}
		if s.captureHook != nil {
			s.captureHook(false, s.root, source, quarantine)
		}
		captured, openErr = s.childRoot(quarantine)
	} else if openErr == nil {
		if _, err := s.root.Lstat(source); err == nil {
			captured.Close()
			return ownershipError("both original and captured image remain, preserving both")
		} else if !errors.Is(err, os.ErrNotExist) {
			captured.Close()
			return err
		}
	}
	if openErr != nil {
		return openErr
	}
	captured.Close()
	checked, err := s.ownedDirectory(receipt, quarantine, begun)
	if err != nil {
		return errors.Join(err, cachefs.RenameNoReplace(s.root, quarantine, source))
	}
	defer checked.Close()
	names, err := directoryEntries(checked)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		var file *os.File
		if name == "rootfs.img" {
			file, err = checkWitness(checked, name, receipt.Disk, 0o444)
		} else {
			file, err = s.checkedMetadata(checked, name, receipt)
		}
		if err != nil {
			return err
		}
		file.Close()
		if err := checked.Remove(name); err != nil {
			return err
		}
	}
	if err := syncRoot(checked); err != nil {
		return err
	}
	if err := receipt.Directory.CheckRoot(checked); err != nil {
		return err
	}
	if err := s.root.Remove(quarantine); err != nil {
		return err
	}
	if err := s.syncDir("."); err != nil {
		return err
	}
	if err := s.removeImageReceipt(receipt); err != nil {
		return err
	}
	report.Removed = append(report.Removed, digest)
	return nil
}

func (s *Store) removeImageReceipt(receipt imageOwnership) error {
	if receipt.Cleanup == "" {
		name, err := randomLeaf(".receipt-purge-")
		if err != nil {
			return err
		}
		receipt.Cleanup = name
		if err := s.writeOwnership(receipt); err != nil {
			return err
		}
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	return removeReceipt(root, imageReceiptName(receipt.Digest), receipt.Cleanup)
}

func removeReceipt(root *os.Root, name, quarantine string) error {
	_, witness, err := readReceipt(root, name)
	if errors.Is(err, os.ErrNotExist) {
		_, witness, err = readReceipt(root, quarantine)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		if err := captureLeaf(root, name, quarantine, witness, 0o600); err != nil {
			return err
		}
	}
	file, err := checkWitness(root, quarantine, witness, 0o600)
	if err != nil {
		return err
	}
	file.Close()
	if err := root.Remove(quarantine); err != nil {
		return err
	}
	return syncRoot(root)
}
