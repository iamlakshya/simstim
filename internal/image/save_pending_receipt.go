package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// savedPendingReceipt is issued only after the actual converter/source joins.
// It contains no scan value or review content. Its native lease stays held
// through review; after a creator crash it permits only exact owned expiry.
type savedPendingReceipt struct {
	Version   int               `json:"v"`
	ID        string            `json:"id"`
	Ref       string            `json:"ref"`
	Digest    string            `json:"digest"`
	Domain    cachefs.Domain    `json:"domain"`
	Store     cachefs.Reference `json:"store"`
	Directory cachefs.Reference `json:"directory"`
	Root      imageFileWitness  `json:"root"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func validPendingSaveID(id string) bool         { return len(id) == 32 && supply.IsHex64(id+id) }
func pendingSaveLease(id string) string         { return "pending-save-" + id + ".lock" }
func pendingSaveHead(id string) string          { return "pending-save-" + id + ".json" }
func pendingSaveExpiry(id string) string        { return "pending-expire-" + id + ".json" }
func pendingSaveRetired(id string) string       { return ".pending-save-retired-" + id + ".json" }
func pendingSaveExpiryRetired(id string) string { return ".pending-expire-retired-" + id + ".json" }
func pendingSaveStage(id string) string         { return "incoming/.image-stage-" + id }
func pendingSaveCaptured(id string) string      { return "incoming/.expired-save-" + id }

func (store *Store) writeSavePending(ctx context.Context, prepared *PreparedSave) (imageFileWitness, error) {
	file, err := witnessFile(prepared.root, "rootfs.img", prepared.output, 0o600, false)
	if err != nil {
		return imageFileWitness{}, err
	}
	receipt := savedPendingReceipt{Version: 1, ID: prepared.id, Ref: prepared.record.Ref, Digest: prepared.record.Digest, Domain: store.domain, Store: store.generation, Directory: prepared.directory, Root: file, CreatedAt: prepared.deadline.Add(-SaveLifetime).UTC(), ExpiresAt: prepared.deadline.UTC()}
	root, err := store.ownershipRoot()
	if err != nil {
		return imageFileWitness{}, err
	}
	defer root.Close()
	var witness imageFileWitness
	err = store.withPublication(ctx, func() error {
		var err error
		witness, err = store.createSavePending(root, pendingSaveHead(receipt.ID), receipt)
		return err
	})
	return witness, err
}

func (store *Store) createSavePending(root *os.Root, name string, receipt savedPendingReceipt) (imageFileWitness, error) {
	data, err := json.Marshal(receipt)
	if err != nil {
		return imageFileWitness{}, err
	}
	file, err := cachefs.OpenFile(root, name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return imageFileWitness{}, err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return imageFileWitness{}, err
	}
	if err := file.Sync(); err != nil {
		return imageFileWitness{}, err
	}
	witness, err := witnessFile(root, name, file, 0o600, true)
	if err != nil {
		return imageFileWitness{}, err
	}
	return witness, syncRoot(root)
}

func (store *Store) readSavePending(root *os.Root, name string) (savedPendingReceipt, imageFileWitness, error) {
	var receipt savedPendingReceipt
	data, witness, err := readReceipt(root, name)
	if err != nil {
		return receipt, witness, err
	}
	if !api.ExactJSONFields(data, receipt) || json.Unmarshal(data, &receipt) != nil || receipt.Version != 1 || !validPendingSaveID(receipt.ID) || !pendingSaveReceiptName(name, receipt.ID) || !ValidSaveRef(receipt.Ref) || !ValidDigest(receipt.Digest) || receipt.Domain != store.domain || receipt.Domain.Validate() != nil || !receipt.Directory.Known() || !receipt.Directory.Directory || !receipt.Root.Ref.Known() || receipt.Root.Ref.Directory || receipt.Root.Ref.Mode != 0o600 || receipt.Root.Ref.Size <= 0 || receipt.Root.Ref.Size > maxOutput || receipt.Root.SHA256 != "" || len(receipt.Root.extra) != 0 || receipt.CreatedAt.IsZero() || !receipt.ExpiresAt.Equal(receipt.CreatedAt.Add(SaveLifetime)) {
		return receipt, witness, ownershipError("pending image receipt is unknown or has another native role")
	}
	if err := receipt.Store.CheckRoot(store.root); err != nil {
		return receipt, witness, err
	}
	return receipt, witness, nil
}

func (store *Store) removeSavePending(id string, expected *imageFileWitness) error {
	root, err := store.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	_, current, err := store.readSavePending(root, pendingSaveHead(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected != nil {
		current = *expected
	}
	return removeSavePendingLeaf(root, pendingSaveHead(id), id, current)
}

func removeSavePendingLeaf(root *os.Root, name, id string, witness imageFileWitness) error {
	quarantine := pendingSaveRetired(id)
	if name == pendingSaveExpiry(id) || name == pendingSaveExpiryRetired(id) {
		quarantine = pendingSaveExpiryRetired(id)
	}
	if name != quarantine {
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

func (store *Store) retireSavePendingLease(id string) error {
	if !validPendingSaveID(id) {
		return ownershipError("pending image lease has no original identifier")
	}
	receipt, err := store.readLeaseOwnership(leaseReceiptName(pendingSaveLease(id)))
	if errors.Is(err, os.ErrNotExist) {
		if _, err := store.root.Lstat(pendingSaveLease(id)); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return store.purgeLease(ctx, receipt)
}

func pendingSaveName(name string) bool {
	return strings.HasPrefix(name, "pending-save-") || strings.HasPrefix(name, "pending-expire-") || strings.HasPrefix(name, ".pending-save-retired-") || strings.HasPrefix(name, ".pending-expire-retired-")
}
func pendingSaveReceiptName(name, id string) bool {
	return name == pendingSaveHead(id) || name == pendingSaveExpiry(id) || name == pendingSaveRetired(id) || name == pendingSaveExpiryRetired(id)
}
