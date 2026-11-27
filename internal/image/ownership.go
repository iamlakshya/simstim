package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

type imageFileWitness struct {
	extra  map[string]json.RawMessage
	Ref    cachefs.Reference `json:"ref"`
	SHA256 string            `json:"sha256,omitempty"`
}
type metadataIntent struct {
	extra      map[string]json.RawMessage
	Temporary  string           `json:"temporary"`
	Quarantine string           `json:"quarantine"`
	Before     imageFileWitness `json:"before"`
	After      imageFileWitness `json:"after"`
}
type purgeIntent struct {
	extra      map[string]json.RawMessage
	Quarantine string `json:"quarantine"`
}
type imageOwnership struct {
	Version   int               `json:"version"`
	Domain    *cachefs.Domain   `json:"domain,omitempty"`
	Digest    string            `json:"platform_digest"`
	Store     cachefs.Reference `json:"store"`
	Directory cachefs.Reference `json:"directory"`
	Leaf      string            `json:"leaf"`
	Disk      imageFileWitness  `json:"rootfs"`
	Metadata  imageFileWitness  `json:"metadata"`
	Pending   *metadataIntent   `json:"pending,omitempty"`
	Cleanup   string            `json:"cleanup,omitempty"`
	Purge     *purgeIntent      `json:"purge,omitempty"`
	extra     map[string]json.RawMessage
}
type leaseOwnership struct {
	Version    int               `json:"version"`
	Domain     *cachefs.Domain   `json:"domain,omitempty"`
	Path       string            `json:"path"`
	Store      cachefs.Reference `json:"store"`
	Parent     cachefs.Reference `json:"parent"`
	File       cachefs.Reference `json:"file"`
	Quarantine string            `json:"quarantine,omitempty"`
	Cleanup    string            `json:"cleanup,omitempty"`
	extra      map[string]json.RawMessage
}

// Tests inject a reported Close failure after the actual owned descriptor is
// closed. Publication state remains authoritative even when Close then fails.
var closeImageMetadata = func(file *os.File) error { return file.Close() }

func ownershipError(what string) error {
	return &proto.Error{Code: "image_ownership", Message: "cannot prove image store ownership: " + what, Hint: "Keep the reported files and inspect the image store before retrying the purge."}
}
func stageLeaf(name string) bool {
	return nonceLeaf(name, ".image-stage-") || nonceLeaf(name, "incoming/.image-stage-")
}
func nonceLeaf(name, prefix string) bool {
	suffix := strings.TrimPrefix(name, prefix)
	return strings.HasPrefix(name, prefix) && len(suffix) == 32 && supply.IsHex64(suffix+suffix)
}
func randomLeaf(prefix string) (string, error) {
	nonce, err := randomName()
	return prefix + nonce, err
}
func imageReceiptName(digest string) string { return "image-" + digest[7:] + ".json" }
func leaseReceiptName(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "lease-" + hex.EncodeToString(sum[:]) + ".json"
}

func (r imageOwnership) known() bool {
	if r.Domain != nil && !r.Domain.Known() {
		return false
	}
	if r.Version != 1 || !ValidDigest(r.Digest) || !r.Store.Known() || !r.Directory.Known() || !r.Disk.Ref.Known() || !r.Metadata.Ref.Known() || !supply.IsHex64(r.Metadata.SHA256) || len(r.extra) != 0 || len(r.Disk.extra) != 0 || len(r.Metadata.extra) != 0 || (r.Cleanup != "" && !nonceLeaf(r.Cleanup, ".receipt-purge-")) {
		return false
	}
	if r.Leaf != r.Digest[7:] && !stageLeaf(r.Leaf) {
		return false
	}
	if p := r.Pending; p != nil {
		if len(p.extra) != 0 || len(p.Before.extra) != 0 || len(p.After.extra) != 0 {
			return false
		}
		if !nonceLeaf(p.Temporary, ".metadata-") || !nonceLeaf(p.Quarantine, ".metadata-old-") || !p.Before.Ref.Known() || !p.After.Ref.Known() || !supply.IsHex64(p.Before.SHA256) || !supply.IsHex64(p.After.SHA256) {
			return false
		}
	}
	return r.Purge == nil || len(r.Purge.extra) == 0 && nonceLeaf(r.Purge.Quarantine, ".image-purge-")
}
func safeReceiptLeaf(leaf string) bool {
	return leaf != "" && leaf != "." && leaf != ".." && !strings.ContainsAny(leaf, "/\\\x00:")
}
func (r leaseOwnership) known() bool {
	if r.Domain != nil && !r.Domain.Known() {
		return false
	}
	if r.Version != 1 || !r.Store.Known() || !r.Parent.Known() || !r.File.Known() || len(r.extra) != 0 || (r.Cleanup != "" && !nonceLeaf(r.Cleanup, ".receipt-purge-")) {
		return false
	}
	_, valid := leaseRole(r.Path)
	return valid && (r.Quarantine == "" || nonceLeaf(r.Quarantine, ".lease-purge-"))
}

func witnessFile(root *os.Root, name string, file *os.File, mode os.FileMode, metadata bool) (imageFileWitness, error) {
	ref, err := cachefs.CaptureFile(root, name, file, mode)
	if err != nil {
		return imageFileWitness{}, err
	}
	witness := imageFileWitness{Ref: ref}
	if metadata {
		witness.SHA256, err = metadataHash(file)
		if err != nil {
			return witness, err
		}
		err = ref.CheckFile(root, name, file, mode)
	}
	return witness, err
}
func metadataHash(file *os.File) (string, error) {
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("rewind image metadata: %w", err)
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, MaxManifest+1))
	if err != nil {
		return "", err
	}
	if n > MaxManifest {
		return "", ownershipError("metadata exceeds its existing 4 MiB bound")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return "", fmt.Errorf("rewind image metadata: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func checkWitness(root *os.Root, name string, w imageFileWitness, mode os.FileMode) (*os.File, error) {
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, mode)
	if err != nil {
		return nil, err
	}
	if err := w.Ref.CheckFile(root, name, file, mode); err != nil {
		file.Close()
		return nil, err
	}
	if w.SHA256 != "" {
		hash, err := metadataHash(file)
		if err != nil || hash != w.SHA256 {
			file.Close()
			return nil, ownershipError("metadata content changed")
		}
		if err := w.Ref.CheckFile(root, name, file, mode); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func (s *Store) ownershipRoot() (*os.Root, error) {
	return cachefs.OpenPrivateChild(s.root, "ownership", false)
}
func (s *Store) readOwnership(digest string) (imageOwnership, error) {
	var receipt imageOwnership
	root, err := s.ownershipRoot()
	if err != nil {
		return receipt, err
	}
	defer root.Close()
	data, _, err := readReceipt(root, imageReceiptName(digest))
	if err != nil {
		return receipt, err
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return receipt, err
	}
	if !receipt.known() || receipt.Digest != digest {
		return receipt, ownershipError("image receipt has unknown fields or witnesses")
	}
	if err := s.checkDomain(receipt.Domain); err != nil {
		return receipt, err
	}
	if err := receipt.Store.CheckRoot(s.root); err != nil {
		return receipt, err
	}
	return receipt, nil
}
func (s *Store) writeOwnership(receipt imageOwnership) error {
	if err := s.checkDomain(receipt.Domain); err != nil {
		return err
	}
	if !receipt.known() {
		return ownershipError("refusing to publish an unknown image receipt")
	}
	root, err := s.ownershipRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return replaceReceipt(root, imageReceiptName(receipt.Digest), data)
}
func readReceipt(root *os.Root, name string) ([]byte, imageFileWitness, error) {
	file, err := cachefs.OpenFile(root, name, os.O_RDONLY, 0o600)
	if err != nil {
		return nil, imageFileWitness{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxManifest+1))
	if err != nil {
		return nil, imageFileWitness{}, err
	}
	if int64(len(data)) > MaxManifest {
		return nil, imageFileWitness{}, ownershipError("ownership receipt exceeds 4 MiB")
	}
	witness, err := witnessFile(root, name, file, 0o600, true)
	return data, witness, err
}

// replaceReceipt changes only a checked existing protocol inode or an absent
// protocol leaf. On a capture/restore collision both files remain for review.
func replaceReceipt(root *os.Root, name string, data []byte) (returned error) {
	_, before, err := readReceipt(root, name)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := randomLeaf(".receipt-new-")
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(root, temporary, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, closeImageMetadata(file)) }()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	after, err := witnessFile(root, temporary, file, 0o600, true)
	if err != nil {
		return err
	}
	quarantine := ""
	if exists {
		quarantine, err = randomLeaf(".receipt-old-")
		if err != nil {
			return err
		}
		if err := captureLeaf(root, name, quarantine, before, 0o600); err != nil {
			return err
		}
	}
	if err := cachefs.RenameNoReplace(root, temporary, name); err != nil {
		if exists {
			err = errors.Join(err, cachefs.RenameNoReplace(root, quarantine, name))
		}
		return err
	}
	if err := after.Ref.CheckFile(root, name, file, 0o600); err != nil {
		return err
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	if quarantine != "" {
		old, err := checkWitness(root, quarantine, before, 0o600)
		if err != nil {
			return err
		}
		old.Close()
		if err := root.Remove(quarantine); err != nil {
			return err
		}
	}
	return syncRoot(root)
}
func captureLeaf(root *os.Root, name, quarantine string, w imageFileWitness, mode os.FileMode) error {
	file, err := checkWitness(root, name, w, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := cachefs.RenameNoReplace(root, name, quarantine); err != nil {
		return err
	}
	captured, err := checkWitness(root, quarantine, w, mode)
	if err == nil {
		captured.Close()
		return syncRoot(root)
	}
	restore := cachefs.RenameNoReplace(root, quarantine, name)
	return errors.Join(ownershipError("captured file changed, preserving it without unlink"), err, restore)
}
func syncRoot(root *os.Root) (returned error) {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, closeImageMetadata(file)) }()
	return cachefs.SyncDirectory(file)
}

func decodeOwnership(data []byte, value any, names []string) (map[string]json.RawMessage, error) {
	fields, err := api.ProjectedJSONObjectFields(context.Background(), data, names...)
	if err != nil {
		return nil, errors.Join(ownershipError("receipt metadata has duplicate or malformed known fields"), err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return nil, err
	}
	for _, name := range names {
		delete(fields, name)
	}
	return fields, nil
}
func encodeOwnership(value any, extra map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range extra {
		if _, ok := fields[name]; !ok {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}
func (r *imageOwnership) UnmarshalJSON(data []byte) error {
	type plain imageOwnership
	var value plain
	extra, err := decodeOwnership(data, &value, []string{"version", "domain", "platform_digest", "store", "directory", "leaf", "rootfs", "metadata", "pending", "purge", "cleanup"})
	*r = imageOwnership(value)
	r.extra = extra
	return err
}
func (r imageOwnership) MarshalJSON() ([]byte, error) {
	type plain imageOwnership
	return encodeOwnership(plain(r), r.extra)
}
func (r *leaseOwnership) UnmarshalJSON(data []byte) error {
	type plain leaseOwnership
	var value plain
	extra, err := decodeOwnership(data, &value, []string{"version", "domain", "path", "store", "parent", "file", "quarantine", "cleanup"})
	*r = leaseOwnership(value)
	r.extra = extra
	return err
}
func (r leaseOwnership) MarshalJSON() ([]byte, error) {
	type plain leaseOwnership
	return encodeOwnership(plain(r), r.extra)
}

func sameMetadata(data []byte, record Record) ([]byte, error) {
	var fields map[string]json.RawMessage
	if len(data) > 0 {
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil, err
		}
	}
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	generated, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	var typed map[string]json.RawMessage
	if err := json.Unmarshal(generated, &typed); err != nil {
		return nil, err
	}
	for name, value := range typed {
		fields[name] = value
	}
	result, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if int64(len(result)) > MaxManifest {
		return nil, ownershipError("image metadata exceeds 4 MiB")
	}
	return bytes.TrimSpace(result), nil
}

func (w *imageFileWitness) UnmarshalJSON(data []byte) error {
	type plain imageFileWitness
	var value plain
	extra, err := decodeOwnership(data, &value, []string{"ref", "sha256"})
	*w = imageFileWitness(value)
	w.extra = extra
	return err
}
func (w imageFileWitness) MarshalJSON() ([]byte, error) {
	type plain imageFileWitness
	return encodeOwnership(plain(w), w.extra)
}
func (p *metadataIntent) UnmarshalJSON(data []byte) error {
	type plain metadataIntent
	var value plain
	extra, err := decodeOwnership(data, &value, []string{"temporary", "quarantine", "before", "after"})
	*p = metadataIntent(value)
	p.extra = extra
	return err
}
func (p metadataIntent) MarshalJSON() ([]byte, error) {
	type plain metadataIntent
	return encodeOwnership(plain(p), p.extra)
}
func (p *purgeIntent) UnmarshalJSON(data []byte) error {
	type plain purgeIntent
	var value plain
	extra, err := decodeOwnership(data, &value, []string{"quarantine"})
	*p = purgeIntent(value)
	p.extra = extra
	return err
}
func (p purgeIntent) MarshalJSON() ([]byte, error) {
	type plain purgeIntent
	return encodeOwnership(plain(p), p.extra)
}
