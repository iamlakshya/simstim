package image

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (s *Store) prepareMetadata(ctx context.Context, root *os.Root, record Record, old []byte) (name string, witness imageFileWitness, returned error) {
	data, err := sameMetadata(old, record)
	if err != nil {
		return "", witness, err
	}
	name, err = randomLeaf(".metadata-")
	if err != nil {
		return "", witness, err
	}
	var file *os.File
	err = s.withCreation(ctx, func() error {
		var createErr error
		file, createErr = cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		return createErr
	})
	if err != nil {
		return "", witness, err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return name, witness, err
	}
	if err := file.Sync(); err != nil {
		return name, witness, err
	}
	witness, err = witnessFile(root, name, file, 0o600, true)
	return name, witness, err
}

func (s *Store) saveFreshRecord(ctx context.Context, dir string, record Record) (imageFileWitness, error) {
	var empty imageFileWitness
	root, err := s.childRoot(dir)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	name, witness, err := s.prepareMetadata(ctx, root, record, nil)
	if err != nil {
		return empty, err
	}
	if err := cachefs.RenameNoReplace(root, name, "image.json"); err != nil {
		return empty, err
	}
	file, err := checkWitness(root, "image.json", witness, 0o600)
	if err != nil {
		return empty, err
	}
	file.Close()
	return witness, syncRoot(root)
}

func (s *Store) replaceOwnedMetadata(ctx context.Context, receipt *imageOwnership, record Record) error {
	root, err := s.ownedDirectory(*receipt, receipt.Leaf, false)
	if err != nil {
		return err
	}
	defer root.Close()
	old, err := checkWitness(root, "image.json", receipt.Metadata, 0o600)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(old, MaxManifest+1))
	old.Close()
	if err != nil {
		return err
	}
	temporary, after, err := s.prepareMetadata(ctx, root, record, data)
	if err != nil {
		return err
	}
	quarantine, err := randomLeaf(".metadata-old-")
	if err != nil {
		return err
	}
	receipt.Pending = &metadataIntent{Temporary: temporary, Quarantine: quarantine, Before: receipt.Metadata, After: after}
	if err := s.writeOwnership(*receipt); err != nil {
		return err
	}
	return s.recoverMetadata(ctx, receipt)
}

func (s *Store) recoverMetadata(ctx context.Context, receipt *imageOwnership) error {
	if receipt.Pending == nil {
		return nil
	}
	if receipt.Purge != nil {
		return ownershipError("metadata belongs to an active image purge")
	}
	root, err := s.childRoot(receipt.Leaf)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := receipt.Directory.CheckRoot(root); err != nil {
		return err
	}
	pending := receipt.Pending
	if current, err := checkWitness(root, "image.json", pending.After, 0o600); err == nil {
		current.Close()
		return s.finishMetadata(root, receipt)
	}
	return s.withPublication(ctx, func() error {
		if err := receipt.Directory.CheckRoot(root); err != nil {
			return err
		}
		current, err := checkWitness(root, "image.json", pending.Before, 0o600)
		if err == nil {
			current.Close()
			if err := captureLeaf(root, "image.json", pending.Quarantine, pending.Before, 0o600); err != nil {
				return err
			}
		} else if errors.Is(err, os.ErrNotExist) {
			captured, err := checkWitness(root, pending.Quarantine, pending.Before, 0o600)
			if err != nil {
				return err
			}
			captured.Close()
		} else {
			return err
		}
		next, err := checkWitness(root, pending.Temporary, pending.After, 0o600)
		if err != nil {
			restore := cachefs.RenameNoReplace(root, pending.Quarantine, "image.json")
			return errors.Join(ownershipError("prepared metadata is missing or changed; both witnesses are retained"), err, restore)
		}
		next.Close()
		if err := cachefs.RenameNoReplace(root, pending.Temporary, "image.json"); err != nil {
			return errors.Join(err, cachefs.RenameNoReplace(root, pending.Quarantine, "image.json"))
		}
		published, err := checkWitness(root, "image.json", pending.After, 0o600)
		if err != nil {
			return err
		}
		published.Close()
		if err := syncRoot(root); err != nil {
			return err
		}
		return s.finishMetadata(root, receipt)
	})
}

func (s *Store) finishMetadata(root *os.Root, receipt *imageOwnership) error {
	pending := receipt.Pending
	old, err := checkWitness(root, pending.Quarantine, pending.Before, 0o600)
	if err == nil {
		old.Close()
		if err := root.Remove(pending.Quarantine); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	receipt.Metadata = pending.After
	receipt.Pending = nil
	return s.writeOwnership(*receipt)
}
