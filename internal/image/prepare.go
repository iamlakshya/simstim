package image

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Converter runs the normaliser and ext4 converter in the canonical jail.
type Converter func(context.Context, []Layer, []*os.File, *os.File) error

func randomName() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}

// Prepare fetches anonymously by the client's exact pins and publishes once.
func (s *Store) Prepare(ctx context.Context, request Request, registry *Registry, convert Converter, progress ...func(Progress)) (returnedRecord Record, returned error) {
	if request.SavedHost != nil {
		return Record{}, saveFailure("image_saved", "a saved root can only be opened from its original host store")
	}
	if err := s.requireWritableHost(); err != nil {
		return Record{}, err
	}
	if err := request.Validate(); err != nil {
		return Record{}, err
	}
	lease, err := s.lock(ctx, request.PlatformDigest[7:]+".lock", false)
	if err != nil {
		return Record{}, err
	}
	defer lease.Close()
	if record, err := s.existingOwned(ctx, request); err == nil {
		return record, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	ref, _ := ParseReference(request.Ref)
	root, err := registry.Manifest(ctx, ref, request.Digest)
	if err != nil {
		return Record{}, err
	}
	platform, err := Platform(root, request.Arch)
	if err != nil {
		return Record{}, err
	}
	if platform != request.PlatformDigest {
		return Record{}, mismatch("image platform")
	}
	doc := root
	if platform != root.Digest {
		doc, err = registry.Manifest(ctx, ref, platform)
		if err != nil {
			return Record{}, err
		}
	}
	layers, err := Layers(doc)
	if err != nil {
		return Record{}, err
	}
	var received, total int64
	for _, layer := range layers {
		total += layer.Size
	}
	note := func(n int64) {
		if len(progress) > 0 && progress[0] != nil {
			progress[0](Progress{Token: request.Transfer, Received: received + n, Total: total})
		}
	}
	note(0)
	files := make([]*os.File, 0, len(layers))
	cleanups := make([]func() error, 0, len(layers))
	defer func() {
		for i, f := range files {
			returned = errors.Join(returned, cleanups[i](), f.Close())
		}
	}()
	for _, layer := range layers {
		file, cleanup, err := s.fetchLayer(ctx, registry, ref, layer, note)
		if err != nil {
			return Record{}, err
		}
		files = append(files, file)
		cleanups = append(cleanups, cleanup)
		received += layer.Size
	}
	return s.publishLocked(ctx, request, layers, files, convert)
}

func (s *Store) fetchLayer(ctx context.Context, registry *Registry, ref Reference, layer Layer, progress func(int64)) (*os.File, func() error, error) {
	lease, err := s.lock(ctx, "incoming/"+layer.Digest[7:]+".lock", false)
	if err != nil {
		return nil, nil, err
	}
	defer lease.Close()
	nonce, err := randomName()
	if err != nil {
		return nil, nil, err
	}
	name := "incoming/" + layer.Digest[7:] + "-" + nonce + ".part"
	var file *os.File
	err = s.withCreation(ctx, func() error {
		var createErr error
		file, createErr = s.regular(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		return createErr
	})
	if err != nil {
		return nil, nil, err
	}
	created, err := s.captureCreated(name, file, 0o600)
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	cleanup := func() error { return s.cleanupCreated(created, 0o600) }
	if err := registry.Blob(ctx, ref, layer, file, progress); err != nil {
		cleanupErr := cleanup()
		file.Close()
		return nil, nil, errors.Join(err, cleanupErr)
	}
	readonly, err := s.regular(name, os.O_RDONLY, 0o600)
	if err != nil {
		cleanupErr := cleanup()
		file.Close()
		return nil, nil, errors.Join(err, cleanupErr)
	}
	// Keep the input named and single-link until conversion has finished. The
	// converter receives only this read-only descriptor, never the write handle.
	created.Descriptor = readonly
	if err := file.Close(); err != nil {
		cleanupErr := cleanup()
		readonly.Close()
		return nil, nil, errors.Join(err, cleanupErr)
	}
	return readonly, cleanup, nil
}

func rootCheck(request Request, record Record) error {
	if request.RootHash != "" && (record.RootHash != request.RootHash || record.DataBlocks != request.DataBlocks) {
		return &proto.Error{Code: "image_root_mismatch", Message: fmt.Sprintf("image %s gave root %s with %d data blocks; the client expects %s with %d; conversion was refused", request.Digest, record.RootHash, record.DataBlocks, request.RootHash, request.DataBlocks), Hint: "Run host doctor and report both root hashes as a bug."}
	}
	return nil
}

func (s *Store) publish(ctx context.Context, request Request, layers []Layer, files []*os.File, convert Converter) (Record, error) {
	lease, err := s.lock(ctx, request.PlatformDigest[7:]+".lock", false)
	if err != nil {
		return Record{}, err
	}
	defer lease.Close()
	return s.publishLocked(ctx, request, layers, files, convert)
}

func (s *Store) publishLocked(ctx context.Context, request Request, layers []Layer, files []*os.File, convert Converter) (Record, error) {
	if current, err := s.existingOwned(ctx, request); err == nil {
		return current, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, err
	}
	nonce, err := randomName()
	if err != nil {
		return Record{}, err
	}
	stage := ".image-stage-" + nonce
	var stageRoot *os.Root
	err = s.withCreation(ctx, func() error {
		var createErr error
		stageRoot, createErr = s.exclusiveDirectory(stage)
		return createErr
	})
	if err != nil {
		return Record{}, err
	}
	defer stageRoot.Close()
	directory, err := cachefs.CaptureRoot(stageRoot)
	if err != nil {
		return Record{}, err
	}
	durable := false
	var out *os.File
	err = s.withCreation(ctx, func() error {
		var createErr error
		out, createErr = s.regular(stage+"/rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		return createErr
	})
	if err != nil {
		return Record{}, errors.Join(err, s.cleanupStage(stage, stageRoot, directory, nil))
	}
	defer out.Close()
	creator, err := s.captureCreated(stage+"/rootfs.img", out, 0o600)
	if err != nil {
		return Record{}, err
	}
	defer func() {
		if !durable {
			_ = s.cleanupStage(stage, stageRoot, directory, map[string]createdImageFile{"rootfs.img": creator})
		}
	}()
	if err := convert(ctx, layers, files, out); err != nil {
		return Record{}, err
	}
	record, err := Inspect(out)
	if err != nil {
		return Record{}, err
	}
	if err := rootCheck(request, record); err != nil {
		return Record{}, err
	}
	record.Ref = request.Ref
	record.Digest = request.Digest
	record.PlatformDigest = request.PlatformDigest
	record.Arch = request.Arch
	record.ConvertedAt = time.Now().UTC()
	record.UsedAt = record.ConvertedAt
	if err := out.Chmod(0o444); err != nil {
		return Record{}, err
	}
	if err := out.Sync(); err != nil {
		return Record{}, err
	}
	metadata, err := s.saveFreshRecord(ctx, stage, record)
	if err != nil {
		return Record{}, err
	}
	disk, err := witnessFile(stageRoot, "rootfs.img", out, 0o444, false)
	if err != nil {
		return Record{}, err
	}
	if disk.Ref.Identity != creator.File.Identity {
		return Record{}, ownershipError("converted output changed creator native identity")
	}
	receipt := imageOwnership{Version: 1, Domain: s.receiptDomain(), Digest: request.PlatformDigest, Store: s.generation, Directory: directory, Leaf: stage, Disk: disk, Metadata: metadata}
	durable = true
	if err := s.publishPrepared(ctx, receipt); err != nil {
		return Record{}, err
	}

	return record, nil
}

func (s *Store) syncDir(name string) error {
	file, err := s.root.Open(name)
	if err != nil {
		return fmt.Errorf("open image folder for sync: %w", err)
	}
	defer file.Close()
	if err := cachefs.SyncDirectory(file); err != nil {
		return fmt.Errorf("sync image folder: %w", err)
	}
	return nil
}

func checkBlob(file *os.File, layer Layer) error {
	st, err := validateBlobDescriptor(file)
	if err != nil {
		return err
	}
	if st.Size() != layer.Size {
		return mismatch("copied layer size")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(file, layer.Size+1))
	if err != nil {
		return err
	}
	if n != layer.Size || "sha256:"+hex.EncodeToString(h.Sum(nil)) != layer.Digest {
		return mismatch("copied layer")
	}
	_, err = file.Seek(0, 0)
	return err
}

// CheckDocuments checks copied manifest bytes against both client pins.
func CheckDocuments(request Request, documents map[string][]byte) ([]Layer, error) {
	if request.SavedHost != nil {
		return nil, saveFailure("image_saved", "a saved root has no registry manifest or transferable layers")
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	root := Document{request.Digest, documents[request.Digest]}
	platform, err := Platform(root, request.Arch)
	if err != nil {
		return nil, err
	}
	if platform != request.PlatformDigest {
		return nil, mismatch("copied platform manifest")
	}
	return Layers(Document{platform, documents[platform]})
}

// DocumentLayer describes manifest bytes in the same bounded transfer format.
func DocumentLayer(doc Document) Layer {
	return Layer{doc.Digest, "application/json", int64(len(doc.Data))}
}

// Bytes verifies a document with the same streaming digest checker as blobs.
func Bytes(doc Document) io.Reader { return bytes.NewReader(doc.Data) }
