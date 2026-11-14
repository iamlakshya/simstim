package image

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"io"
	"os"
	"sync"
)

// Transfer owns fresh copied blobs until conversion commits or the bridge ends.
// Callers cannot name host paths. Every blob is checked against OCI metadata.
type Transfer struct {
	mu            sync.Mutex
	store         *Store
	request       Request
	token         string
	files         map[string]*os.File
	sizes         map[string]int64
	received      int64
	closed        bool
	directory     cachefs.Reference
	directoryRoot *os.Root
	creators      map[string]createdImageFile
}

// Begin starts a fresh private laptop transfer under a random connection token.
func (s *Store) Begin(request Request) (*Transfer, error) {
	if !s.domain.IsZero() {
		return nil, imageDomainError("host image transfer requires its owned operation context")
	}
	return s.BeginContext(context.Background(), request)
}

// BeginContext admits a new transfer through the caller's operation and the
// platform's exact domain lease before creating its private incoming directory.
func (s *Store) BeginContext(ctx context.Context, request Request) (*Transfer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.requireWritableHost(); err != nil {
		return nil, err
	}
	if err := request.Validate(); err != nil {
		return nil, err
	}
	if !s.domain.IsZero() {
		lease, err := s.lock(ctx, request.PlatformDigest[7:]+".lock", false)
		if err != nil {
			return nil, err
		}
		defer lease.Close()
	}
	token, err := randomName()
	if err != nil {
		return nil, err
	}
	parent, leaf, err := s.parent("incoming/" + token)
	if err != nil {
		return nil, err
	}
	if err := s.withCreation(ctx, func() error { return parent.Mkdir(leaf, 0o700) }); err != nil {
		parent.Close()
		return nil, err
	}
	root, err := cachefs.OpenPrivateChild(parent, leaf, false)
	parent.Close()
	if err != nil {
		return nil, err
	}
	directory, err := cachefs.CaptureRoot(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Transfer{store: s, request: request, token: token, files: map[string]*os.File{}, sizes: map[string]int64{}, directory: directory, directoryRoot: root, creators: map[string]createdImageFile{}}, nil
}

// Progress reports bounded received bytes without accepting a filesystem name.
func (t *Transfer) Progress() Progress {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Progress{Token: t.token, Received: t.received}
}

// WriteAt accepts contiguous chunks only, preventing sparse or overlapping writes.
func (t *Transfer) WriteAt(ctx context.Context, digest string, offset int64, data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("image transfer has ended")
	}
	if !ValidDigest(digest) || offset < 0 || len(data) == 0 || len(data) > MaxChunk || offset != t.sizes[digest] {
		return badLayer("image copy has an invalid digest, chunk or offset")
	}
	if int64(len(data)) > MaxCompressed+2*MaxManifest-t.received {
		return tooLarge("image copy exceeds the compressed layer and manifest caps")
	}
	lease, err := t.store.lock(ctx, "incoming/"+digest[7:]+".lock", false)
	if err != nil {
		return err
	}
	defer lease.Close()
	file := t.files[digest]
	if file == nil {
		if len(t.files) >= MaxLayers+2 {
			return tooLarge("image copy has more than 128 layers and two manifests")
		}
		name := "incoming/" + t.token + "/" + digest[7:] + ".part"
		err = t.store.withCreation(ctx, func() error {
			var createErr error
			file, createErr = t.store.regular(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			return createErr
		})
		if err != nil {
			return err
		}
		created, err := t.store.captureCreated(name, file, 0o600)
		if err != nil {
			file.Close()
			return err
		}
		t.creators[digest] = created
		t.files[digest] = file
	}
	n, err := file.WriteAt(data, offset)
	t.sizes[digest] += int64(n)
	t.received += int64(n)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (t *Transfer) document(digest string) ([]byte, error) {
	file := t.files[digest]
	if file == nil {
		return nil, badLayer("image copy lacks a pinned manifest")
	}
	if t.sizes[digest] > MaxManifest {
		return nil, badLayer("copied manifest exceeds 4 MiB")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, MaxManifest+1))
}

// Commit rechecks every copied digest before calling the jailed converter.
func (t *Transfer) Commit(ctx context.Context, convert Converter) (Record, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return Record{}, fmt.Errorf("image transfer has ended")
	}
	defer t.close()
	documents := map[string][]byte{}
	for _, digest := range []string{t.request.Digest, t.request.PlatformDigest} {
		data, err := t.document(digest)
		if err != nil {
			return Record{}, err
		}
		documents[digest] = data
	}
	layers, err := CheckDocuments(t.request, documents)
	if err != nil {
		return Record{}, err
	}
	files := make([]*os.File, len(layers))
	defer func() {
		for _, file := range files {
			if file != nil {
				file.Close()
			}
		}
	}()
	for i, layer := range layers {
		file := t.files[layer.Digest]
		if file == nil {
			if layer.Size != 0 {
				return Record{}, badLayer("image copy lacks a layer")
			}
			return Record{}, badLayer("image copy has an empty layer")
		}
		if err := checkBlob(file, layer); err != nil {
			return Record{}, err
		}
		opened, err := t.store.regular("incoming/"+t.token+"/"+layer.Digest[7:]+".part", os.O_RDONLY, 0o600)
		if err != nil {
			return Record{}, err
		}
		files[i] = opened
	}
	return t.store.publish(ctx, t.request, layers, files, convert)
}

// Close removes partial blobs and closes all descriptors on failure or disconnect.
func (t *Transfer) Close() error { t.mu.Lock(); defer t.mu.Unlock(); return t.close() }
func (t *Transfer) close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	if t.directoryRoot != nil {
		defer t.directoryRoot.Close()
	}
	var result error
	for digest, file := range t.files {
		if created, ok := t.creators[digest]; ok {
			result = errors.Join(result, t.store.cleanupCreated(created, 0o600))
		} else {
			result = errors.Join(result, ownershipError("copied image part lacks its creator witness"))
		}
		result = errors.Join(result, file.Close())
	}
	root, err := t.store.childRoot("incoming/" + t.token)
	if err != nil {
		return errors.Join(result, err)
	}
	defer root.Close()
	if err := t.directory.CheckRoot(root); err != nil {
		return errors.Join(result, err)
	}
	names, err := directoryEntries(root)
	if err != nil {
		return errors.Join(result, err)
	}
	if len(names) != 0 {
		return errors.Join(result, ownershipError("image transfer retains unknown or replaced files"))
	}
	// Reopen the named parent immediately before rmdir. An empty replacement
	// token directory must not be mistaken for the creator's original root.
	named, err := t.store.childRoot("incoming/" + t.token)
	if err != nil {
		return errors.Join(result, err)
	}
	checkErr := t.directory.CheckRoot(named)
	named.Close()
	if checkErr != nil {
		return errors.Join(result, checkErr)
	}
	return errors.Join(result, t.store.root.Remove("incoming/"+t.token))
}
