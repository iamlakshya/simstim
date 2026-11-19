package image

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
)

// SavedHostV1 identifies the root bytes kept on their one original host.
// This public metadata supplies neither a pathname nor native store authority.
// The actual store additionally requires its original checked native receipt.
type SavedHostV1 struct {
	Version int    `json:"v"`
	HostID  string `json:"host_id"`
	Ref     string `json:"ref"`
	Digest  string `json:"digest"`
}

// UnmarshalJSON closes this versioned identity, including field aliases and
// repeated keys. Unsupported metadata cannot fall through to OCI authority.
func (identity *SavedHostV1) UnmarshalJSON(raw []byte) error {
	type wire SavedHostV1
	var value wire
	if !api.ExactJSONFields(raw, wire{}) || json.Unmarshal(raw, &value) != nil {
		return saveProtocol("the saved-host image identity is not a closed versioned object")
	}
	*identity = SavedHostV1(value)
	return identity.Validate()
}

// CloneSavedHostV1 owns the optional typed identity.
func CloneSavedHostV1(input *SavedHostV1) *SavedHostV1 {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

// Clone owns every mutable value in an image request.
func (request Request) Clone() Request {
	request.SavedHost = CloneSavedHostV1(request.SavedHost)
	return request
}

// Validate checks public identity syntax only. It grants no root-file access.
func (identity SavedHostV1) Validate() error {
	if identity.Version != 1 || len(identity.HostID) != 32 || !supply.IsHex64(identity.HostID+identity.HostID) || !ValidSaveRef(identity.Ref) || !ValidDigest(identity.Digest) {
		return saveProtocol("the saved-host image identity is invalid")
	}
	return nil
}

func validateSavedRequest(request Request) error {
	if request.SavedHost == nil || request.SavedHost.Validate() != nil || request.Ref != request.SavedHost.Ref || request.Digest != request.SavedHost.Digest || request.PlatformDigest != request.Digest || request.Transfer != "" || request.Arch != "amd64" && request.Arch != "arm64" || !supply.IsHex64(request.RootHash) || request.DataBlocks <= 0 || request.DataBlocks > MaxUncompressed/4096 {
		return saveProtocol("the saved-image request differs from its original host pin")
	}
	return nil
}

func checkSavedSelection(request Request, record Record) error {
	if request.SavedHost == nil && record.SavedHost == nil {
		return nil
	}
	if request.SavedHost == nil || record.SavedHost == nil || request.SavedHost.Validate() != nil || record.SavedHost.Validate() != nil || *request.SavedHost != *record.SavedHost || record.Digest != request.Digest || record.PlatformDigest != request.PlatformDigest || record.Ref != request.Ref {
		return saveProtocol("the original saved-host identity differs from the checked image record")
	}
	return nil
}

func projectSavedSelection(request Request, record Record) (Record, error) {
	if request.SavedHost == nil && record.SavedHost == nil {
		return record, nil
	}
	if request.SavedHost == nil || record.SavedHost == nil || request.SavedHost.Validate() != nil || record.SavedHost.Validate() != nil || request.SavedHost.HostID != record.SavedHost.HostID || request.SavedHost.Digest != record.SavedHost.Digest || !slices.Contains(record.SavedAliases, request.Ref) {
		return Record{}, saveProtocol("the requested alias has no original saved-root producer record")
	}
	record.Ref, record.SavedHost = request.Ref, CloneSavedHostV1(request.SavedHost)
	record.SavedAliases = slices.Clone(record.SavedAliases)
	return record, checkSavedSelection(request, record)
}

func (store *Store) checkSavedRequestDomain(request Request) error {
	if request.SavedHost == nil {
		return nil
	}
	if store.domain.IsZero() || store.domain.Validate() != nil || request.SavedHost.HostID != store.domain.HostID {
		return saveFailure("image_elsewhere", "the saved image belongs to another host or has no original host store")
	}
	return nil
}

func (store *Store) checkSavedRecord(record Record, _ *os.File) error {
	if record.SavedHost == nil {
		return nil
	}
	if record.SavedHost.Validate() != nil || record.Ref != record.SavedHost.Ref || record.Digest != record.SavedHost.Digest || record.PlatformDigest != record.Digest || store.domain.IsZero() || store.domain.Validate() != nil || store.domain.HostID != record.SavedHost.HostID {
		return saveProtocol("the stored saved root has no matching original host identity")
	}
	if len(record.SavedAliases) == 0 || len(record.SavedAliases) > MaxEntries || !slices.IsSorted(record.SavedAliases) || !slices.Contains(record.SavedAliases, record.Ref) {
		return saveProtocol("the saved root has an invalid alias producer record")
	}
	for index, ref := range record.SavedAliases {
		if !ValidSaveRef(ref) || index > 0 && ref == record.SavedAliases[index-1] {
			return saveProtocol("the saved root has an invalid or repeated alias")
		}
	}
	return nil
}

func (store *Store) checkSavedDescriptor(ctx context.Context, record Record, root *os.File) error {
	if record.SavedHost == nil {
		return nil
	}
	if err := store.checkSavedRecord(record, root); err != nil {
		return err
	}
	receipt, err := store.readOwnership(record.Digest)
	if err != nil {
		return err
	}
	if receipt.Leaf != record.Digest[7:] || receipt.Pending != nil || receipt.Purge != nil {
		return ownershipError("the saved root has no stable native producer receipt")
	}
	directory, err := store.ownedDirectory(receipt, receipt.Leaf, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := receipt.Disk.Ref.CheckFile(directory, "rootfs.img", root, 0o444); err != nil {
		return err
	}
	hash, err := savedRootDigest(ctx, root)
	if err != nil {
		return err
	}
	if hash != record.Digest {
		return mismatch("the original saved root bytes")
	}
	return receipt.Disk.Ref.CheckFile(directory, "rootfs.img", root, 0o444)
}

func savedRootDigest(ctx context.Context, root *os.File) (string, error) {
	if root == nil {
		return "", saveProtocol("the saved root descriptor is missing")
	}
	info, err := root.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxOutput {
		return "", saveProtocol("the saved root descriptor has an invalid type or size")
	}
	digest := sha256.New()
	count, err := io.Copy(digest, &flatContextReader{ctx, io.NewSectionReader(root, 0, info.Size())})
	if err != nil {
		return "", err
	}
	if count != info.Size() {
		return "", saveProtocol("the saved root changed during its digest check")
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
