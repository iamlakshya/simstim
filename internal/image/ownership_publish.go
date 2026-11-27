package image

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (s *Store) ownedDirectory(receipt imageOwnership, leaf string, missing bool) (*os.Root, error) {
	if err := s.checkDomain(receipt.Domain); err != nil {
		return nil, err
	}
	if err := receipt.Store.CheckRoot(s.root); err != nil {
		return nil, err
	}
	root, err := s.childRoot(leaf)
	if err != nil {
		return nil, err
	}
	if err := receipt.Directory.CheckRoot(root); err != nil {
		root.Close()
		return nil, err
	}
	entries, err := directoryEntries(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	allowed := map[string]bool{"rootfs.img": true, "image.json": true}
	if receipt.Pending != nil {
		allowed[receipt.Pending.Temporary] = true
		allowed[receipt.Pending.Quarantine] = true
	}
	for _, name := range entries {
		if !allowed[name] {
			root.Close()
			return nil, ownershipError("image directory contains an unreceipted entry")
		}
	}
	disk, err := checkWitness(root, "rootfs.img", receipt.Disk, 0o444)
	if err != nil && !(missing && errors.Is(err, os.ErrNotExist)) {
		root.Close()
		return nil, err
	}
	if disk != nil {
		info, err := Inspect(disk)
		disk.Close()
		if err != nil {
			root.Close()
			return nil, err
		}
		var metadata Record
		metadataFile, err := s.checkedMetadata(root, "image.json", receipt)
		if errors.Is(err, os.ErrNotExist) && receipt.Pending != nil {
			metadataFile, err = s.checkedMetadata(root, receipt.Pending.Quarantine, receipt)
			if errors.Is(err, os.ErrNotExist) {
				metadataFile, err = s.checkedMetadata(root, receipt.Pending.Temporary, receipt)
			}
		}
		if err == nil {
			data, readErr := io.ReadAll(io.LimitReader(metadataFile, MaxManifest+1))
			metadataFile.Close()
			if readErr != nil || json.Unmarshal(data, &metadata) != nil || metadata.PlatformDigest != receipt.Digest || metadata.RootHash != info.RootHash || metadata.DataBlocks != info.DataBlocks || metadata.Size != info.Size {
				root.Close()
				return nil, ownershipError("image metadata differs from its checked root descriptor")
			}
		} else if !(missing && errors.Is(err, os.ErrNotExist)) {
			root.Close()
			return nil, err
		}
	}
	for _, name := range entries {
		if name == "rootfs.img" {
			continue
		}
		file, err := s.checkedMetadata(root, name, receipt)
		if err != nil {
			root.Close()
			return nil, err
		}
		file.Close()
	}
	return root, nil
}

func (s *Store) checkedMetadata(root *os.Root, name string, receipt imageOwnership) (*os.File, error) {
	if name == "image.json" {
		file, err := checkWitness(root, name, receipt.Metadata, 0o600)
		if err == nil {
			return file, nil
		}
		if receipt.Pending != nil {
			return checkWitness(root, name, receipt.Pending.After, 0o600)
		}
		return nil, err
	}
	if p := receipt.Pending; p != nil {
		if name == p.Temporary {
			return checkWitness(root, name, p.After, 0o600)
		}
		if name == p.Quarantine {
			return checkWitness(root, name, p.Before, 0o600)
		}
	}
	return nil, ownershipError("unrecognised metadata leaf")
}

func directoryEntries(root *os.Root) ([]string, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names, nil
}

func (s *Store) recoverPublication(ctx context.Context, receipt *imageOwnership) error {
	if receipt.Purge != nil {
		return ownershipError("an image purge is already in progress")
	}
	if receipt.Leaf == receipt.Digest[7:] {
		return nil
	}
	target := receipt.Digest[7:]
	if root, err := s.ownedDirectory(*receipt, target, false); err == nil {
		root.Close()
		receipt.Leaf = target
		return s.writeOwnership(*receipt)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	root, err := s.ownedDirectory(*receipt, receipt.Leaf, false)
	if err != nil {
		return err
	}
	root.Close()
	return s.withPublication(ctx, func() error {
		root, err := s.ownedDirectory(*receipt, receipt.Leaf, false)
		if err != nil {
			return err
		}
		root.Close()
		if err := s.renameNoReplace(receipt.Leaf, target); err != nil {
			return err
		}
		captured, err := s.ownedDirectory(*receipt, target, false)
		if err != nil {
			return errors.Join(err, s.renameNoReplace(target, receipt.Leaf))
		}
		captured.Close()
		if err := s.syncDir("."); err != nil {
			return err
		}
		if parent := path.Dir(receipt.Leaf); parent != "." {
			if err := s.syncDir(parent); err != nil {
				return err
			}
		}
		receipt.Leaf = target
		return s.writeOwnership(*receipt)
	})
}

func (s *Store) existingOwned(ctx context.Context, request Request) (Record, error) {
	receipt, ownErr := s.loadOwnership(ctx, request.PlatformDigest)
	owned := ownErr == nil
	if ownErr != nil && !errors.Is(ownErr, os.ErrNotExist) {
		return Record{}, ownErr
	}
	if !owned && !s.domain.IsZero() {
		if _, err := s.root.Lstat(request.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
			return Record{}, imageDomainError("the existing host image has no current domain receipt")
		}
		return Record{}, os.ErrNotExist
	}
	if owned {
		if err := s.recoverPublication(ctx, &receipt); err != nil {
			return Record{}, err
		}
		if err := s.recoverMetadata(ctx, &receipt); err != nil {
			return Record{}, err
		}
	}
	current, err := s.readRecord(request.PlatformDigest[7:])
	if err != nil {
		return Record{}, err
	}
	if current.Arch != request.Arch {
		return Record{}, badLayer("stored image architecture differs")
	}
	projected, err := projectSavedSelection(request, current)
	if err != nil {
		return Record{}, err
	}
	if err := rootCheck(request, projected); err != nil {
		return Record{}, err
	}
	if current.SavedHost != nil {
		file, err := s.regular(request.PlatformDigest[7:]+"/rootfs.img", os.O_RDONLY, 0o444)
		if err != nil {
			return Record{}, err
		}
		checked := s.checkSavedDescriptor(ctx, projected, file)
		if err := errors.Join(checked, file.Close()); err != nil {
			return Record{}, err
		}
		current.UsedAt = time.Now().UTC()
		if !owned {
			return Record{}, ownershipError("a saved root cannot use unreceipted legacy metadata")
		}
		if err := s.replaceOwnedMetadata(ctx, &receipt, current); err != nil {
			return Record{}, err
		}
		projected.UsedAt = current.UsedAt
		return projected, nil
	}
	current.Digest = request.Digest
	current.Ref = request.Ref
	// A matching legacy pair remains usable but is never adopted by a UsedAt
	// rewrite. Only the producer receipt authorises a replacement metadata inode.
	if !owned {
		return current, nil
	}
	current.UsedAt = time.Now().UTC()
	if err := s.replaceOwnedMetadata(ctx, &receipt, current); err != nil {
		return Record{}, err
	}
	return current, nil
}

func (s *Store) publishPrepared(ctx context.Context, receipt imageOwnership) error {
	if old, err := s.loadOwnership(ctx, receipt.Digest); err == nil {
		if old.Purge != nil || old.Pending != nil || stageLeaf(old.Leaf) {
			return ownershipError("an earlier producer intent remains")
		}
		if _, err := s.root.Lstat(old.Leaf); !errors.Is(err, os.ErrNotExist) {
			return ownershipError("an earlier image namespace remains")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.writeOwnership(receipt); err != nil {
		return err
	}
	return s.recoverPublication(ctx, &receipt)
}

// cleanupStage uses creator-held descriptors before any durable publication
// intent exists. Unknown children or replaced inodes stay for inspection.
func (s *Store) cleanupStage(leaf string, root *os.Root, directory cachefs.Reference, files map[string]createdImageFile) error {
	if err := directory.CheckRoot(root); err != nil {
		return err
	}
	for _, part := range files {
		file := part.Descriptor
		info, err := file.Stat()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if cachefs.ModeMatches(info, 0o444) {
			mode = 0o444
		}
		if err := s.cleanupCreated(part, mode); err != nil {
			return err
		}

	}
	names, err := directoryEntries(root)
	if err != nil {
		return err
	}
	if len(names) != 0 {
		return ownershipError("conversion staging contains unreceipted files")
	}
	check, err := s.childRoot(leaf)
	if err != nil {
		return err
	}
	defer check.Close()
	if err := directory.CheckRoot(check); err != nil {
		return err
	}
	if err := s.root.Remove(leaf); err != nil {
		return fmt.Errorf("remove empty owned conversion staging: %w", err)
	}
	return s.syncDir(".")
}
